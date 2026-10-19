package hosts_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

const sentinel = "glpat-SENTINEL-0123456789"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type remotes map[string]any // path -> string URL or error

func (r remotes) RemoteURL(_ context.Context, dir, _ string) (string, error) {
	switch v := r[dir].(type) {
	case string:
		return v, nil
	case error:
		return "", v
	}
	return "", git.ErrNoRemote
}

type recorder struct {
	mu   sync.Mutex
	msgs []api.HostsChangedMessage
}

func (r *recorder) PublishHostsChanged(m api.HostsChangedMessage) {
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	r.mu.Unlock()
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type rig struct {
	svc  *hosts.Service
	fake *hoststest.Fake
	clk  *clock
	rec  *recorder
	logs *safeBuf
	rem  remotes
}

func newRig(t *testing.T, env string, rem remotes) *rig {
	t.Helper()
	vars, _ := hosts.ParseEnvText(env)
	cfg := hosts.ParseConfig(vars)
	fake := &hoststest.Fake{}
	clk := &clock{t: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	rec := &recorder{}
	logs := &safeBuf{}
	// Deliberately a plain handler (no redacting wrapper): the service itself
	// must not put a token in a log line.
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := hosts.New(hosts.Deps{
		Config: cfg, EnvPath: "data/.env",
		Clients: map[corehosts.Kind]hosts.HostClient{corehosts.KindGitLab: fake, corehosts.KindGitHub: fake, corehosts.KindGitea: fake},
		Remotes: rem, Publish: rec, Log: log, Clock: clk.now, TickEvery: 5 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); svc.Close() })
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return &rig{svc: svc, fake: fake, clk: clk, rec: rec, logs: logs, rem: rem}
}

const gitlabEnv = "YGG_HOST_1=gitlab https://git.acme.dev\nYGG_HOST_1_TOKEN=" + sentinel + "\n"

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (r *rig) repoView(t *testing.T, id core.RepoID) api.RepoHostView {
	t.Helper()
	v, err := r.svc.RepoHost(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func hasData(v api.RepoHostView) bool {
	switch ph := v.Entry.Phase.(type) {
	case corehosts.Idle:
		return ph.Data != nil
	}
	return false
}

func TestServiceLinksAFetchesAndShowsData(t *testing.T) {
	rem := remotes{"/w/shop-api": "git@git.acme.dev:acme-shop/shop-api.git"}
	r := newRig(t, gitlabEnv, rem)
	r.fake.Script(hoststest.PipelineFailed)
	r.svc.SetRepos([]hosts.RepoRef{{ID: "rep_1", Path: "/w/shop-api", Branch: "feature/cart"}})
	eventually(t, "data", func() bool { return hasData(r.repoView(t, "rep_1")) })

	v := r.repoView(t, "rep_1")
	rs := v.Entry.Phase.(corehosts.Idle).Data.(corehosts.RepoState)
	if !v.Linked || v.Project != "acme-shop/shop-api" || v.Branch != "feature/cart" || v.Instance.ID != "git.acme.dev" || rs.MR == nil || rs.Pipeline.Status != corehosts.PipelineFailed {
		t.Fatalf("view = %+v", v)
	}
	if v.Links.MRList != "https://git.acme.dev/acme-shop/shop-api/-/merge_requests" {
		t.Errorf("links = %+v", v.Links)
	}
	calls := r.fake.Calls()
	var repoCall hoststest.Call
	for _, c := range calls {
		if !c.Inbox {
			repoCall = c
		}
	}
	if repoCall.Token != sentinel || repoCall.Project != "acme-shop/shop-api" || repoCall.Branch != "feature/cart" {
		t.Errorf("call = %+v", repoCall)
	}
	failed, _ := r.svc.FailedPipelines(context.Background())
	if _, ok := failed["rep_1"]; !ok || len(failed) != 1 {
		t.Errorf("failed pipelines = %v", failed)
	}
	r.rec.mu.Lock()
	defer r.rec.mu.Unlock()
	if len(r.rec.msgs) == 0 {
		t.Error("nothing was published")
	}
}

func TestServiceNotLinkedReasons(t *testing.T) {
	rem := remotes{
		"/w/none":     git.ErrNoRemote,
		"/w/unknown":  "https://git.other.dev/acme-shop/shop-api.git",
		"/w/weird":    "/srv/git/shop-api.git",
		"/w/detached": "https://git.acme.dev/acme-shop/shop-api.git",
		"/w/broken":   errors.New("boom " + sentinel),
	}
	r := newRig(t, gitlabEnv, rem)
	r.svc.SetRepos([]hosts.RepoRef{
		{ID: "none", Path: "/w/none", Branch: "main"}, {ID: "unknown", Path: "/w/unknown", Branch: "main"},
		{ID: "weird", Path: "/w/weird", Branch: "main"}, {ID: "detached", Path: "/w/detached", Branch: ""},
		{ID: "broken", Path: "/w/broken", Branch: "main"},
	})
	for _, id := range []core.RepoID{"none", "unknown", "weird", "detached", "broken"} {
		eventually(t, string(id), func() bool {
			return r.repoView(t, id).NotLinked != "Yggdrasil hasn't looked at this repo's remote yet."
		})
		v := r.repoView(t, id)
		if v.Linked || v.NotLinked == "" {
			t.Errorf("%s: view = %+v", id, v)
		}
		if strings.Contains(v.NotLinked, sentinel) || strings.Contains(v.NotLinked, "/srv") {
			t.Errorf("%s: reason leaks: %q", id, v.NotLinked)
		}
	}
	if !strings.Contains(r.repoView(t, "unknown").NotLinked, "git.other.dev") || !strings.Contains(r.repoView(t, "detached").NotLinked, "detached") {
		t.Errorf("reasons: %q / %q", r.repoView(t, "unknown").NotLinked, r.repoView(t, "detached").NotLinked)
	}
	if strings.Contains(r.logs.String(), sentinel) {
		t.Fatalf("a log line holds the token:\n%s", r.logs.String())
	}
}

func TestServiceFlagsCredentialsInRemoteWithoutEchoingThem(t *testing.T) {
	rem := remotes{"/w/a": "https://oauth2:SECRET-pw-77@git.acme.dev/acme-shop/shop-api.git"}
	r := newRig(t, gitlabEnv, rem)
	r.svc.SetRepos([]hosts.RepoRef{{ID: "a", Path: "/w/a", Branch: "main"}})
	eventually(t, "data", func() bool { return hasData(r.repoView(t, "a")) })
	v := r.repoView(t, "a")
	if !v.CredentialsInRemote || !v.Linked {
		t.Fatalf("view = %+v", v)
	}
	for _, c := range r.fake.Calls() {
		if strings.Contains(fmt.Sprintf("%+v", c), "SECRET") {
			t.Errorf("call holds the password: %+v", c)
		}
	}
	if s := fmt.Sprintf("%+v", v) + r.logs.String(); strings.Contains(s, "SECRET") {
		t.Fatal("the remote's credentials leaked")
	}
}

// S3: per instance, never more than 4 requests are in flight.
func TestServiceNeverRunsMoreThanFourRequestsPerInstance(t *testing.T) {
	rem := remotes{}
	var refs []hosts.RepoRef
	for i := 0; i < 12; i++ {
		p := fmt.Sprintf("/w/r%d", i)
		rem[p] = fmt.Sprintf("https://git.acme.dev/acme-shop/repo-%d.git", i)
		refs = append(refs, hosts.RepoRef{ID: core.RepoID(fmt.Sprintf("r%02d", i)), Path: p, Branch: "main"})
	}
	r := newRig(t, gitlabEnv, rem)
	r.fake.Script(hoststest.OpenSuccess)
	r.fake.SetDelay(20 * time.Millisecond)
	r.svc.SetRepos(refs)
	eventually(t, "all repos fetched", func() bool {
		for _, ref := range refs {
			if !hasData(r.repoView(t, ref.ID)) {
				return false
			}
		}
		return true
	})
	if got := r.fake.MaxInFlight(); got > corehosts.MaxInFlight || got < 2 {
		t.Fatalf("max in flight = %d, want 2..%d", got, corehosts.MaxInFlight)
	}
}

// 401 blocks the key until the user refreshes; the data stays.
func TestServiceUnauthorizedBlocksUntilRefresh(t *testing.T) {
	rem := remotes{"/w/a": "https://git.acme.dev/acme-shop/shop-api.git"}
	r := newRig(t, gitlabEnv, rem)
	r.fake.Script(hoststest.Unauthorized)
	r.svc.SetRepos([]hosts.RepoRef{{ID: "a", Path: "/w/a", Branch: "main"}})
	isBlocked := func() bool {
		_, ok := r.repoView(t, "a").Entry.Phase.(corehosts.Blocked)
		return ok
	}
	eventually(t, "blocked", isBlocked)
	n := len(r.fake.Calls())
	r.clk.advance(time.Hour)
	time.Sleep(60 * time.Millisecond)
	if got := len(r.fake.Calls()); got != n {
		t.Fatalf("a blocked key kept fetching: %d -> %d calls", n, got)
	}
	r.fake.Script(hoststest.OpenSuccess)
	if err := r.svc.Refresh(context.Background(), []corehosts.Key{corehosts.RepoKey{Repo: "a"}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "data after refresh", func() bool { return hasData(r.repoView(t, "a")) })
}

func TestServiceTransientFailureBacksOffThenRecovers(t *testing.T) {
	rem := remotes{"/w/a": "https://git.acme.dev/acme-shop/shop-api.git"}
	r := newRig(t, gitlabEnv, rem)
	r.fake.Script(hoststest.ServerError)
	r.svc.SetRepos([]hosts.RepoRef{{ID: "a", Path: "/w/a", Branch: "main"}})
	eventually(t, "waiting", func() bool {
		w, ok := r.repoView(t, "a").Entry.Phase.(corehosts.Waiting)
		return ok && w.Why == corehosts.WaitTransient
	})
	r.fake.Script(hoststest.OpenSuccess)
	r.clk.advance(corehosts.Backoff(1) + time.Second)
	eventually(t, "recovered", func() bool { return hasData(r.repoView(t, "a")) })
}

func TestServiceInboxMergesAndNeedsToken(t *testing.T) {
	r := newRig(t, gitlabEnv+"YGG_HOST_2=gitea https://code.example.org\n", remotes{})
	r.fake.Script(hoststest.InboxRoles)
	eventually(t, "inbox", func() bool {
		v, _ := r.svc.Inbox(context.Background())
		return len(v.Items) > 0
	})
	v, _ := r.svc.Inbox(context.Background())
	// Only git.acme.dev has a token, so only it has an inbox; github.com and
	// gitlab.com, gitea without a token have none.
	if len(v.Instances) != 1 || v.Instances[0].Instance != "git.acme.dev" || len(v.Items) != 3 {
		t.Fatalf("inbox = %+v", v)
	}
	insts, _ := r.svc.Instances(context.Background())
	var tokened int
	for _, in := range insts {
		if in.Token {
			tokened++
		}
	}
	if tokened != 1 || len(insts) != 4 {
		t.Errorf("instances = %+v", insts)
	}
	info := r.svc.ConfigInfo(context.Background())
	if info.EnvPath != "data/.env" {
		t.Errorf("info = %+v", info)
	}
}

func TestServiceRefreshRejectsUnknownKeys(t *testing.T) {
	r := newRig(t, gitlabEnv, remotes{})
	for _, k := range []corehosts.Key{corehosts.RepoKey{Repo: "nope"}, corehosts.InboxKey{Instance: "nowhere.dev"}} {
		err := r.svc.Refresh(context.Background(), []corehosts.Key{k})
		if c, ok := apperr.ClassOf(err); !ok || c != apperr.Invalid {
			t.Errorf("Refresh(%v) = %v", k, err)
		}
	}
}

// S2: a sentinel token never shows up in a log line, a published message, a
// view or an error, including when a client fails and echoes it.
func TestServiceSentinelTokenNeverLeaks(t *testing.T) {
	rem := remotes{"/w/a": "https://git.acme.dev/acme-shop/shop-api.git", "/w/b": "git@git.acme.dev:acme-shop/other.git"}
	r := newRig(t, gitlabEnv, rem)
	mode := 0
	var mu sync.Mutex
	r.fake.SetRepoFn(func(_ context.Context, _ corehosts.Instance, token string, _ corehosts.ProjectPath, _ string, _ string) (corehosts.RepoState, string, error) {
		mu.Lock()
		defer mu.Unlock()
		mode++
		switch mode % 4 {
		case 0:
			return corehosts.RepoState{}, "", &hosts.FetchError{Kind: corehosts.FailTransient, Err: errors.New("GET failed with Authorization: Bearer " + token)}
		case 1:
			return corehosts.RepoState{}, "", errors.New("could not parse: token=" + token)
		case 2:
			return corehosts.RepoState{}, "", &hosts.FetchError{Kind: corehosts.FailUnauthorized, Err: errors.New(token)}
		}
		return corehosts.RepoState{MR: &corehosts.MergeRequest{Number: 1, Title: "t"}}, "", nil
	})
	r.fake.SetInboxFn(func(_ context.Context, _ corehosts.Instance, token string) ([]corehosts.InboxItem, error) {
		return nil, errors.New("inbox failed " + token)
	})
	r.svc.SetRepos([]hosts.RepoRef{{ID: "a", Path: "/w/a", Branch: "main"}, {ID: "b", Path: "/w/b", Branch: "main"}})
	var views strings.Builder
	for i := 0; i < 40; i++ {
		r.clk.advance(20 * time.Minute)
		if i%5 == 0 {
			_ = r.svc.Refresh(context.Background(), nil)
		}
		time.Sleep(8 * time.Millisecond)
		for _, id := range []core.RepoID{"a", "b"} {
			fmt.Fprintf(&views, "%+v\n", r.repoView(t, id))
		}
		in, _ := r.svc.Inbox(context.Background())
		insts, _ := r.svc.Instances(context.Background())
		fmt.Fprintf(&views, "%+v %+v %+v\n", in, insts, r.svc.ConfigInfo(context.Background()))
	}
	r.rec.mu.Lock()
	fmt.Fprintf(&views, "%+v", r.rec.msgs)
	r.rec.mu.Unlock()
	if len(r.fake.Calls()) < 4 {
		t.Fatalf("too few calls (%d) to mean anything", len(r.fake.Calls()))
	}
	for name, s := range map[string]string{"logs": r.logs.String(), "views": views.String()} {
		if strings.Contains(s, sentinel) {
			t.Errorf("the token is in the %s", name)
		}
	}
	// The same holds through the process-wide redacting handler.
	if got := safety.NewRedactor(sentinel).Redact(r.logs.String()); strings.Contains(got, sentinel) {
		t.Error("redactor misses the token")
	}
}

func TestServiceSetReposBeforeStartAndClose(t *testing.T) {
	svc := hosts.New(hosts.Deps{Config: hosts.ParseConfig(nil)})
	svc.SetRepos([]hosts.RepoRef{{ID: "a", Path: "/x", Branch: "main"}})
	if v, _ := svc.RepoHost(context.Background(), "a"); v.Linked || v.NotLinked == "" {
		t.Fatalf("view = %+v", v)
	}
	if err := svc.Refresh(context.Background(), nil); err == nil {
		t.Error("Refresh before Start succeeded")
	}
	svc.Close() // no-op, must not hang or panic
}
