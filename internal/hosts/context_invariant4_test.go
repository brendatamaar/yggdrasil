package hosts

// TestInvariant_4_HostContextRedacted: tokens never appear in URLs, git config,
// logs or the audit log (docs/AGENTS.md section 6, invariant 4), here for host
// context reads (GH-05/GH-06). A sentinel credential is the configured token;
// the host side puts it in every string field, error body and redirect it can,
// and nothing of it may reach the returned payload, an error, the logs, the
// core entry, or a foreign server. The hosts package writes no audit entry; its
// only outputs are the values checked here.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

const sentinelCredential = "sentinel-credential-7f3a91"

// identityFields are validated closed or numeric values the core checks against
// the key (and MR state is a closed set), not free text: the injector sets them
// to valid values instead of the sentinel.
var identityFields = map[string]bool{"RunID": true, "Head": true, "JobID": true, "State": true, "Reason": true}

// inject sets every free-text string field reachable from v to carry the
// sentinel, so a field added later is covered without editing this test.
func inject(v reflect.Value, path string) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString("pre-" + sentinelCredential + "-post " + path)
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if identityFields[f.Name] || !v.Field(i).CanSet() {
				continue
			}
			inject(v.Field(i), path+"."+f.Name)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			inject(v.Index(i), fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

// strings collects every string reachable from v.
func collectStrings(v reflect.Value, out *[]string) {
	switch v.Kind() {
	case reflect.String:
		*out = append(*out, v.String())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			collectStrings(v.Field(i), out)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			collectStrings(v.Index(i), out)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			collectStrings(v.Elem(), out)
		}
	}
}

func filled[T any](seed T) T {
	p := reflect.New(reflect.TypeOf(seed))
	p.Elem().Set(reflect.ValueOf(seed))
	inject(p.Elem(), "")
	return p.Elem().Interface().(T)
}

func noSentinel(t *testing.T, what string, v any) {
	t.Helper()
	var all []string
	collectStrings(reflect.ValueOf(v), &all)
	all = append(all, fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v))
	for _, s := range all {
		if strings.Contains(s, sentinelCredential) {
			t.Fatalf("%s carries the credential: %.120q", what, s)
		}
	}
}

type captureHandler struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(&h.buf, "%s", r.Message)
	r.Attrs(func(a slog.Attr) bool { fmt.Fprintf(&h.buf, " %s=%v", a.Key, a.Value); return true })
	h.buf.WriteByte('\n')
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }
func (h *captureHandler) text() string                       { h.mu.Lock(); defer h.mu.Unlock(); return h.buf.String() }

func TestInvariant_4_HostContextRedacted(t *testing.T) {
	logs := &captureHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(old) })
	// Negative control: the capture sees a leak when there is one.
	slog.Info("control", "token", sentinelCredential)
	if !strings.Contains(logs.text(), sentinelCredential) {
		t.Fatal("the log capture does not work")
	}
	logs.mu.Lock()
	logs.buf.Reset()
	logs.mu.Unlock()

	t.Run("every string field a client returns is redacted before it is kept", func(t *testing.T) {
		h := corehosts.JobLogs{RunID: "12", Head: contractHeadA, Jobs: []corehosts.JobLog{{JobID: "101"}, {JobID: "102"}}}
		mr := corehosts.MRContext{MR: corehosts.MergeRequest{Number: 3, State: corehosts.MROpen}, Head: contractHeadA}
		issue := corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 5, Labels: []string{"a", "b"}}}
		list := corehosts.IssueList{Items: []corehosts.IssueSummary{{Number: 1, Labels: []string{"a"}}, {Number: 2, Labels: []string{"b", "c"}}}}
		gap := []corehosts.ContextUnavailable{{Source: "x", Reason: corehosts.ContextNotFound}}
		cases := []struct {
			key corehosts.ContextKey
			res ContextResult
		}{
			{issuesKey(), ContextResult{Value: filled(list), Unavailable: filled(gap)}},
			{issueKey(), ContextResult{Value: filled(issue)}},
			{jobsKey(), ContextResult{Value: filled(h), Unavailable: filled(gap)}},
			{mrKey(), ContextResult{Value: filled(mr)}},
		}
		for _, c := range cases {
			// Control: the injector really put the sentinel in.
			var before []string
			collectStrings(reflect.ValueOf(c.res.Value), &before)
			if !strings.Contains(strings.Join(before, " "), sentinelCredential) {
				t.Fatalf("%s: the injector set nothing", c.key.Kind)
			}
			hh := newContextHarness(t, func(int) (ContextResult, error) { return ContextResult{}, nil })
			hh.setToken(sentinelCredential)
			hh.client.issue = func(int) (ContextResult, error) { return c.res, nil }
			hh.client.mr = func(int) (ContextResult, error) { return c.res, nil }
			hh.client.jobs = func(string, string) (ContextResult, error) { return c.res, nil }
			hh.client.script = func(int) (ContextResult, error) { return c.res, nil }
			out, err := hh.readKey(c.key)
			if err != nil || out.Result == nil {
				t.Fatalf("%s: %+v %v", c.key.Kind, out, err)
			}
			noSentinel(t, string(c.key.Kind)+" outcome", out.Result)
			hh.c.mu.Lock()
			entry := hh.c.slots[c.key].entry
			hh.c.mu.Unlock()
			noSentinel(t, string(c.key.Kind)+" core entry", entry)
			// A cached answer is clean too.
			again, err := hh.readKey(c.key)
			if err != nil || again.Result == nil {
				t.Fatal(err)
			}
			noSentinel(t, string(c.key.Kind)+" cached outcome", again.Result)
		}
	})

	t.Run("errors and blocked answers never carry the credential", func(t *testing.T) {
		leak := "status 500 for token " + sentinelCredential
		for name, err := range map[string]error{
			"transient":    &FetchError{Kind: corehosts.FailTransient, Err: errors.New(leak)},
			"unauthorized": &FetchError{Kind: corehosts.FailUnauthorized, Err: errors.New(leak)},
			"plain":        errors.New(leak),
			"invalid":      fmt.Errorf("%w: %s", ErrContextInvalid, leak),
		} {
			hh := newContextHarness(t, func(int) (ContextResult, error) { return ContextResult{}, err })
			hh.setToken(sentinelCredential)
			out, rerr := hh.read()
			if rerr != nil {
				noSentinel(t, name+" error", rerr.Error())
			}
			noSentinel(t, name+" outcome", out)
			hh.c.mu.Lock()
			entry := hh.c.slots[hh.key].entry
			hh.c.mu.Unlock()
			noSentinel(t, name+" core entry", entry)
		}
	})

	t.Run("over real HTTP: provider fields, error bodies, headers and redirects", func(t *testing.T) {
		var foreignMu sync.Mutex
		var foreignSaw []string
		foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			foreignMu.Lock()
			foreignSaw = append(foreignSaw, r.URL.String(), fmt.Sprint(r.Header))
			foreignMu.Unlock()
		}))
		defer foreign.Close()
		head := contractHeadA
		for _, lab := range []bool{false, true} {
			srv, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
				p := r.URL.Path
				w.Header().Set("Link", `<https://x.test/?token=`+sentinelCredential+`>; rel="next-ignored"`)
				switch {
				case strings.HasSuffix(p, "/issues") && lab:
					fmt.Fprintf(w, `[{"iid":1,"title":"t %[1]s","author":{"username":"u-%[1]s"},"labels":["l-%[1]s"],"updated_at":"2026-03-01T10:00:00Z"}]`, sentinelCredential)
				case strings.HasSuffix(p, "/issues"):
					fmt.Fprintf(w, `[{"number":1,"title":"t %[1]s","user":{"login":"u-%[1]s"},"labels":[{"name":"l-%[1]s"}],"updated_at":"2026-03-01T10:00:00Z"}]`, sentinelCredential)
				case strings.HasSuffix(p, "/issues/5"):
					// A host that echoes the credential in an error body.
					w.WriteHeader(500)
					fmt.Fprint(w, "token "+sentinelCredential)
				case strings.HasSuffix(p, "/trace") || strings.HasSuffix(p, "/logs"):
					// A log redirect into a foreign origin, with the credential in its URL.
					http.Redirect(w, r, foreign.URL+"/log?token="+sentinelCredential, http.StatusFound)
				case strings.HasSuffix(p, "/jobs"):
					if lab {
						fmt.Fprint(w, `[{"id":7,"name":"n","status":"failed"}]`)
					} else {
						fmt.Fprint(w, `{"jobs":[{"id":7,"name":"n","conclusion":"failure"}]}`)
					}
				case strings.Contains(p, "/pipelines/") || strings.Contains(p, "/runs/"):
					fmt.Fprintf(w, `{"id":12,"sha":%q,"head_sha":%q}`, head, head)
				case strings.HasSuffix(p, "/pulls/3") || strings.HasSuffix(p, "/merge_requests/3"):
					// A redirect of a plain read to a foreign host, credential in the URL.
					http.Redirect(w, r, foreign.URL+"/x?token="+sentinelCredential, http.StatusFound)
				default:
					t.Errorf("unexpected %s", p)
				}
			})
			_ = in
			cfg.tokens[in.ID] = sentinelCredential
			root := srv.URL
			getter := NewGetter(srv.Client(), nil)
			client := realClient{ContextAPI: ContextAPI{Getter: getter, Root: root, GitLab: lab}}
			svc := New(Deps{Config: cfg, Clients: map[corehosts.Kind]HostClient{in.Kind: client}})
			cs := NewContextService(svc, contractResolver(in), WithContextSleeper(&fakeContextSleeper{clock: &fakeContextClock{t: time.Unix(1, 0)}}))

			out, err := cs.Read(context.Background(), issuesKey())
			if err != nil || out.Result == nil {
				t.Fatalf("lab=%v issues: %+v %v", lab, out, err)
			}
			noSentinel(t, "issues payload", out.Result)
			if !strings.Contains(fmt.Sprint(out.Result.Value), "t [REDACTED]") && !strings.Contains(fmt.Sprint(out.Result.Value), "REDACTED") {
				t.Fatalf("the redaction left no marker: %v", out.Result.Value)
			}
			for _, key := range []corehosts.ContextKey{issueKey(), jobsKey(), mrKey()} {
				out, err = cs.Read(context.Background(), key)
				if err != nil {
					noSentinel(t, string(key.Kind)+" error", err.Error())
				}
				noSentinel(t, string(key.Kind)+" outcome", out)
				cs.mu.Lock()
				if s := cs.slots[key]; s != nil {
					noSentinel(t, string(key.Kind)+" core entry", s.entry)
				}
				cs.mu.Unlock()
			}
		}
		foreignMu.Lock()
		defer foreignMu.Unlock()
		if len(foreignSaw) != 0 {
			t.Fatalf("a foreign server was called: %v", foreignSaw)
		}
	})

	if got := logs.text(); strings.Contains(got, sentinelCredential) {
		t.Fatalf("the credential reached the logs: %q", got)
	}
}
