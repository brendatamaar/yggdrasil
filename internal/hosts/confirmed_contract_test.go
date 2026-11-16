package hosts

// Contract suites for the confirmed host capabilities (AC-01/02/05, NR-04,
// GH-06; TM1-H): CommentHost and RepoHost, each run against a fake (an
// in-memory model of the host) and ConfirmedHosts over a stateful HTTP host
// stub that answers in the GitHub and GitLab REST shapes. The scenarios and
// their expectations are written once, here; neither side defines its own.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// ---- comments ----

type note struct {
	ID   int64
	Body string
}

type commentWorld interface {
	host() CommentHost
	setHead(string)
	setNotes([]note)
	setIncomplete()
	setPost(status int, body string)
	setScope(ok bool)
	posts() int
	lastPost() string
}

const (
	commentMR         = 7
	testCommentMarker = "0123456789abcdef"
)

func bodyHash(body string) string {
	d := sha256.Sum256([]byte(body))
	return hex.EncodeToString(d[:])
}

func suffix(body string, head string) string {
	return body + "\n\n" + "<!-- ygg:" + testCommentMarker + " head:" + head + " -->"
}

// fakeCommentHost models the host and the reader's contract.
type fakeCommentHost struct {
	mu         sync.Mutex
	head       string
	notes      []note
	incomplete bool
	status     int
	reply      string
	scope      bool
	nPosts     int
	last       string
}

func (f *fakeCommentHost) host() CommentHost { return f }
func (f *fakeCommentHost) setHead(h string)  { f.mu.Lock(); f.head = h; f.mu.Unlock() }
func (f *fakeCommentHost) setNotes(n []note) { f.mu.Lock(); f.notes = n; f.mu.Unlock() }
func (f *fakeCommentHost) setIncomplete()    { f.mu.Lock(); f.incomplete = true; f.mu.Unlock() }
func (f *fakeCommentHost) setPost(s int, b string) {
	f.mu.Lock()
	f.status, f.reply = s, b
	f.mu.Unlock()
}
func (f *fakeCommentHost) setScope(ok bool) { f.mu.Lock(); f.scope = ok; f.mu.Unlock() }
func (f *fakeCommentHost) posts() int       { f.mu.Lock(); defer f.mu.Unlock(); return f.nPosts }
func (f *fakeCommentHost) lastPost() string { f.mu.Lock(); defer f.mu.Unlock(); return f.last }

func (f *fakeCommentHost) MRHead(context.Context, core.RepoID, int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.scope {
		return "", ErrContextScope
	}
	return f.head, nil
}

func (f *fakeCommentHost) PostReviewComment(_ context.Context, _ core.RepoID, _ int, head, body, marker string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.scope || f.head != head {
		return "", ErrContextScope
	}
	if strings.Contains(body, "sentinel-secret") {
		return "", errors.New("comment contains a credential")
	}
	f.nPosts++
	f.last = body + "\n\n<!-- ygg:" + marker + " head:" + head + " -->"
	switch {
	case f.status == 400 || f.status == 422:
		return "", ErrWriteRejected
	case f.status == 401 || f.status == 403:
		return "", fetchErr(corehosts.FailUnauthorized, "refused")
	case f.status < 200 || f.status >= 300:
		return "", ErrOutcomeUnknown
	}
	var w struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal([]byte(f.reply), &w) != nil || w.ID <= 0 {
		return "", ErrOutcomeUnknown
	}
	return strconv.FormatInt(w.ID, 10), nil
}

func (f *fakeCommentHost) FindReviewComment(_ context.Context, _ core.RepoID, _ int, marker, hash, head string) (coreproposals.CommentLookup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	unknown := coreproposals.CommentLookup{Outcome: coreproposals.CommentUnknown}
	if !f.scope {
		return unknown, ErrContextScope
	}
	if f.head != head {
		return coreproposals.CommentLookup{Outcome: coreproposals.CommentConflict}, nil
	}
	found := ""
	for _, n := range f.notes {
		if !strings.Contains(n.Body, "ygg:"+marker) {
			continue
		}
		body, ok := strings.CutSuffix(n.Body, "\n\n<!-- ygg:"+marker+" head:"+head+" -->")
		if !ok || bodyHash(body) != hash || found != "" {
			return coreproposals.CommentLookup{Outcome: coreproposals.CommentConflict}, nil
		}
		found = strconv.FormatInt(n.ID, 10)
	}
	if f.incomplete {
		return unknown, nil
	}
	if found != "" {
		return coreproposals.CommentLookup{Outcome: coreproposals.CommentFound, CommentID: found}, nil
	}
	return coreproposals.CommentLookup{Outcome: coreproposals.CommentAbsent}, nil
}

// httpCommentWorld is a stateful host stub plus ConfirmedHosts.
type httpCommentWorld struct {
	fakeCommentHost // state only; its methods are not used for the host
	h               *ConfirmedHosts
	scopeRev        atomic.Int32
	lab             bool
	pageFails       bool
}

func newHTTPCommentWorld(t *testing.T, lab bool) *httpCommentWorld {
	w := &httpCommentWorld{lab: lab}
	w.scope = true
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/pulls/7"):
			fmt.Fprintf(rw, `{"number":7,"head":{"sha":%q}}`, w.head)
		case strings.HasSuffix(p, "/merge_requests/7"):
			fmt.Fprintf(rw, `{"iid":7,"diff_refs":{"head_sha":%q}}`, w.head)
		case r.Method == http.MethodPost && (strings.HasSuffix(p, "/comments") || strings.HasSuffix(p, "/notes")):
			raw, _ := io.ReadAll(r.Body)
			var in struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(raw, &in)
			w.nPosts++
			w.last = in.Body
			rw.WriteHeader(w.status)
			fmt.Fprint(rw, w.reply)
		case strings.HasSuffix(p, "/comments") || strings.HasSuffix(p, "/notes"):
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 {
				page = 1
			}
			if w.incomplete {
				rw.Header().Set("Link", `<next>; rel="next"`)
			}
			if w.pageFails {
				fmt.Fprint(rw, "not json")
				return
			}
			lo, hi := (page-1)*50, min(page*50, len(w.notes))
			if lo > len(w.notes) {
				lo = len(w.notes)
			}
			var out []map[string]any
			for _, n := range w.notes[lo:hi] {
				out = append(out, map[string]any{"id": n.ID, "body": n.Body})
			}
			if out == nil {
				out = []map[string]any{}
			}
			if hi < len(w.notes) {
				rw.Header().Set("Link", `<next>; rel="next"`)
			}
			_ = json.NewEncoder(rw).Encode(out)
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
		}
	}))
	t.Cleanup(srv.Close)
	kind := "github"
	if lab {
		kind = "gitlab"
	}
	cfg := ParseConfig(map[string]Var{"YGG_HOST_1": {Value: kind + " " + srv.URL}, "YGG_HOST_1_TOKEN": {Value: "sentinel-secret"}})
	var inst corehosts.Instance
	for _, in := range cfg.CoreInstances() {
		if in.Base == srv.URL {
			inst = in
		}
	}
	resolve := contextResolverFunc(func(context.Context, corehosts.ContextKey) (ContextTarget, error) {
		w.mu.Lock()
		ok := w.scope
		w.mu.Unlock()
		if !ok {
			return ContextTarget{}, ErrContextScope
		}
		return ContextTarget{Instance: inst.ID, Project: "acme/shop", Revision: "r1"}, nil
	})
	w.h = NewConfirmedHosts(New(Deps{Config: cfg}), resolve, srv.Client())
	return w
}

func (w *httpCommentWorld) host() CommentHost { return w.h }
func (w *httpCommentWorld) setIncomplete() {
	w.mu.Lock()
	w.incomplete = true
	w.mu.Unlock()
}

func commentWorlds(t *testing.T) map[string]commentWorld {
	return map[string]commentWorld{
		"fake":            &fakeCommentHost{scope: true, status: 201, reply: `{"id":99}`},
		"recorded/github": newHTTPCommentWorld(t, false),
		"recorded/gitlab": newHTTPCommentWorld(t, true),
	}
}

func freshComment(t *testing.T, name string) commentWorld {
	w := commentWorlds(t)[name]
	w.setHead(contractHeadA)
	if hw, ok := w.(*httpCommentWorld); ok {
		hw.status, hw.reply = 201, `{"id":99}`
	}
	return w
}

var commentWorldNames = []string{"fake", "recorded/github", "recorded/gitlab"}

func TestCommentHostContract(t *testing.T) {
	ctx := context.Background()
	const body = "Review text"
	post := func(w commentWorld, head string) (string, error) {
		return w.host().PostReviewComment(ctx, "rep_1", commentMR, head, body, testCommentMarker)
	}
	find := func(w commentWorld, head string) (coreproposals.CommentLookup, error) {
		return w.host().FindReviewComment(ctx, "rep_1", commentMR, testCommentMarker, bodyHash(body), head)
	}
	scenarios := []struct {
		name string
		run  func(t *testing.T, w commentWorld)
	}{
		{"one POST with the immutable marker, returns the comment id", func(t *testing.T, w commentWorld) {
			id, err := post(w, contractHeadA)
			if err != nil || id != "99" || w.posts() != 1 {
				t.Fatalf("id %q err %v posts %d", id, err, w.posts())
			}
			if got := w.lastPost(); got != suffix(body, contractHeadA) {
				t.Fatalf("posted %q", got)
			}
		}},
		{"a head that moved before the POST posts nothing", func(t *testing.T, w commentWorld) {
			if _, err := post(w, contractHeadB); err == nil || w.posts() != 0 {
				t.Fatalf("err %v posts %d", err, w.posts())
			}
		}},
		{"a scope loss before the POST posts nothing", func(t *testing.T, w commentWorld) {
			w.setScope(false)
			if _, err := post(w, contractHeadA); !errors.Is(err, ErrContextScope) || w.posts() != 0 {
				t.Fatalf("err %v posts %d", err, w.posts())
			}
		}},
		{"a credential in the body is refused before the POST", func(t *testing.T, w commentWorld) {
			if _, err := w.host().PostReviewComment(ctx, "rep_1", commentMR, contractHeadA, "key sentinel-secret", testCommentMarker); err == nil || w.posts() != 0 {
				t.Fatalf("err %v posts %d", err, w.posts())
			}
		}},
		{"a lost reply is unknown after exactly one POST, then the search finds it", func(t *testing.T, w commentWorld) {
			w.setPost(503, "")
			w.setNotes([]note{{99, suffix(body, contractHeadA)}})
			if _, err := post(w, contractHeadA); !errors.Is(err, ErrOutcomeUnknown) || w.posts() != 1 {
				t.Fatalf("err %v posts %d", err, w.posts())
			}
			got, err := find(w, contractHeadA)
			if err != nil || got.Outcome != coreproposals.CommentFound || got.CommentID != "99" || w.posts() != 1 {
				t.Fatalf("%+v %v posts %d", got, err, w.posts())
			}
		}},
		{"a 2xx reply with no valid comment id is unknown, never a guess", func(t *testing.T, w commentWorld) {
			for _, reply := range []string{`{}`, `{"id":0}`, `{"id":-4}`, `nope`} {
				w.setPost(201, reply)
				if _, err := post(w, contractHeadA); !errors.Is(err, ErrOutcomeUnknown) {
					t.Fatalf("reply %q: %v", reply, err)
				}
			}
		}},
		{"a definite refusal (422) is a rejection, 401 a token problem; one POST each", func(t *testing.T, w commentWorld) {
			w.setPost(422, "")
			if _, err := post(w, contractHeadA); !errors.Is(err, ErrWriteRejected) {
				t.Fatal(err)
			}
			w.setPost(401, "")
			var fe *FetchError
			if _, err := post(w, contractHeadA); !errors.As(err, &fe) || fe.Kind != corehosts.FailUnauthorized {
				t.Fatal(err)
			}
			if w.posts() != 2 {
				t.Fatalf("%d posts", w.posts())
			}
		}},
		{"no comment: absent", func(t *testing.T, w commentWorld) {
			w.setNotes(nil)
			if got, err := find(w, contractHeadA); err != nil || got.Outcome != coreproposals.CommentAbsent {
				t.Fatalf("%+v %v", got, err)
			}
		}},
		{"an unrelated comment does not count", func(t *testing.T, w commentWorld) {
			w.setNotes([]note{{1, "looks good"}})
			if got, err := find(w, contractHeadA); err != nil || got.Outcome != coreproposals.CommentAbsent {
				t.Fatalf("%+v %v", got, err)
			}
		}},
		{"a changed body under our marker is a conflict", func(t *testing.T, w commentWorld) {
			w.setNotes([]note{{5, suffix(body+" edited", contractHeadA)}})
			if got, _ := find(w, contractHeadA); got.Outcome != coreproposals.CommentConflict {
				t.Fatalf("%+v", got)
			}
		}},
		{"our marker with another head is a conflict", func(t *testing.T, w commentWorld) {
			w.setNotes([]note{{5, suffix(body, contractHeadB)}})
			if got, _ := find(w, contractHeadA); got.Outcome != coreproposals.CommentConflict {
				t.Fatalf("%+v", got)
			}
		}},
		{"two matching comments are a conflict", func(t *testing.T, w commentWorld) {
			w.setNotes([]note{{5, suffix(body, contractHeadA)}, {6, suffix(body, contractHeadA)}})
			if got, _ := find(w, contractHeadA); got.Outcome != coreproposals.CommentConflict {
				t.Fatalf("%+v", got)
			}
		}},
		{"found on a later page", func(t *testing.T, w commentWorld) {
			var many []note
			for i := 1; i <= 60; i++ {
				many = append(many, note{int64(i), "chatter"})
			}
			many[55] = note{56, suffix(body, contractHeadA)}
			w.setNotes(many)
			if got, err := find(w, contractHeadA); err != nil || got.Outcome != coreproposals.CommentFound || got.CommentID != "56" {
				t.Fatalf("%+v %v", got, err)
			}
		}},
		{"a list that never ends is unknown, never absent", func(t *testing.T, w commentWorld) {
			w.setNotes(nil)
			w.setIncomplete()
			if got, _ := find(w, contractHeadA); got.Outcome != coreproposals.CommentUnknown {
				t.Fatalf("%+v", got)
			}
		}},
		{"a moved head makes the search a conflict, not absent", func(t *testing.T, w commentWorld) {
			w.setNotes(nil)
			if got, _ := find(w, contractHeadB); got.Outcome != coreproposals.CommentConflict {
				t.Fatalf("%+v", got)
			}
		}},
		{"a scope loss ends the search without a conclusive outcome", func(t *testing.T, w commentWorld) {
			w.setScope(false)
			got, err := find(w, contractHeadA)
			if !errors.Is(err, ErrContextScope) || got.Outcome == coreproposals.CommentFound || got.Outcome == coreproposals.CommentAbsent {
				t.Fatalf("%+v %v", got, err)
			}
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			for _, name := range commentWorldNames {
				t.Run(name, func(t *testing.T) { sc.run(t, freshComment(t, name)) })
			}
		})
	}
}

// ---- repositories ----

type repoWorld interface {
	host() RepoHost
	instance() corehosts.InstanceID
	setOwner(kind string, canCreate bool)
	setRepo(r *repoState)
	setPost(status int, body string)
	posts() int
}

// repoState is what the host holds for acme/shop.
type repoState struct {
	ID      int64
	Owner   string
	Private bool
	Desc    string
	// Refs: "empty", "nonempty", "empty-409" (the host says 409 and branches and
	// tags are empty), "incomplete".
	Refs string
}

const repoDesc = "Created by Yggdrasil [ygg:aaaaaaaaaaaaaaaa]"

func repoSpec(id corehosts.InstanceID, kind string) CreateRepoSpec {
	return CreateRepoSpec{Instance: id, Owner: "acme", OwnerKind: kind, Name: "shop", Description: repoDesc, Private: true}
}

type fakeRepoHost struct {
	mu        sync.Mutex
	id        corehosts.InstanceID
	kind      string
	canCreate bool
	repo      *repoState
	status    int
	reply     string // "ok", "malformed" or a body
	nPosts    int
}

func (f *fakeRepoHost) host() RepoHost                 { return f }
func (f *fakeRepoHost) instance() corehosts.InstanceID { return f.id }
func (f *fakeRepoHost) setOwner(k string, c bool) {
	f.mu.Lock()
	f.kind, f.canCreate = k, c
	f.mu.Unlock()
}
func (f *fakeRepoHost) setRepo(r *repoState)    { f.mu.Lock(); f.repo = r; f.mu.Unlock() }
func (f *fakeRepoHost) setPost(s int, b string) { f.mu.Lock(); f.status, f.reply = s, b; f.mu.Unlock() }
func (f *fakeRepoHost) posts() int              { f.mu.Lock(); defer f.mu.Unlock(); return f.nPosts }
func (f *fakeRepoHost) ResolveOwner(context.Context, corehosts.InstanceID, string) (RepoOwnerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return RepoOwnerInfo{Kind: f.kind, CanCreate: f.canCreate}, nil
}
func (f *fakeRepoHost) CreateRepo(_ context.Context, spec CreateRepoSpec) (CreatedRepo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if spec.OwnerKind != f.kind || !f.canCreate {
		return CreatedRepo{}, errors.New("repository creation permission is unavailable")
	}
	f.nPosts++
	switch {
	case f.status == 400 || f.status == 422:
		return CreatedRepo{}, ErrWriteRejected
	case f.status == 401 || f.status == 403:
		return CreatedRepo{}, fetchErr(corehosts.FailUnauthorized, "refused")
	case f.status < 200 || f.status >= 300 || f.reply == "malformed":
		return CreatedRepo{}, ErrOutcomeUnknown
	}
	return CreatedRepo{RemoteID: "42", URL: "https://git.acme.test/acme/shop.git"}, nil
}
func (f *fakeRepoHost) LookupRepo(_ context.Context, spec CreateRepoSpec) (RepoLookup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo
	if r == nil {
		return RepoLookup{Outcome: RepoUnknown}, fetchErr(corehosts.FailNotFound, "404 hides private repositories")
	}
	if r.Owner != spec.Owner || r.Private != spec.Private || r.Desc != spec.Description {
		return RepoLookup{Outcome: RepoConflict}, nil
	}
	switch r.Refs {
	case "empty", "empty-409":
		return RepoLookup{Outcome: RepoExactMatch, RemoteID: strconv.FormatInt(r.ID, 10), URL: "https://git.acme.test/acme/shop.git"}, nil
	case "nonempty":
		return RepoLookup{Outcome: RepoConflict}, nil
	}
	return RepoLookup{Outcome: RepoUnknown}, nil
}

type httpRepoWorld struct {
	fakeRepoHost
	h  *ConfirmedHosts
	id corehosts.InstanceID
}

func newHTTPRepoWorld(t *testing.T) *httpRepoWorld {
	w := &httpRepoWorld{}
	w.status, w.reply = 201, "ok"
	var base string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, "/api/v3")
		if r.Method == http.MethodPost {
			if p != "/user/repos" && p != "/orgs/acme/repos" {
				t.Errorf("POST to %s", p)
			}
			w.nPosts++
			rw.WriteHeader(w.status)
			if w.reply == "malformed" {
				fmt.Fprint(rw, `{"id":0}`)
				return
			}
			rp := w.repo
			if rp == nil {
				rp = &repoState{ID: 42, Owner: "acme", Private: true, Desc: repoDesc}
			}
			fmt.Fprintf(rw, `{"id":%d,"name":"shop","owner":{"login":%q},"private":%v,"description":%q,"clone_url":"%s/acme/shop.git"}`, rp.ID, rp.Owner, rp.Private, rp.Desc, base)
			return
		}
		switch {
		case p == "/users/acme":
			typ := "User"
			if w.kind == "org" {
				typ = "Organization"
			}
			fmt.Fprintf(rw, `{"login":"acme","type":%q}`, typ)
		case p == "/user":
			who := "acme"
			if !w.canCreate {
				who = "someone-else"
			}
			fmt.Fprintf(rw, `{"login":%q}`, who)
		case p == "/user/memberships/orgs/acme":
			role := "member"
			if w.canCreate {
				role = "admin"
			}
			fmt.Fprintf(rw, `{"state":"active","role":%q}`, role)
		case p == "/repos/acme/shop":
			rp := w.repo
			if rp == nil {
				rw.WriteHeader(404)
				return
			}
			fmt.Fprintf(rw, `{"id":%d,"name":"shop","owner":{"login":%q},"private":%v,"description":%q,"clone_url":"%s/acme/shop.git"}`, rp.ID, rp.Owner, rp.Private, rp.Desc, base)
		case strings.HasSuffix(p, "/git/matching-refs/"):
			switch w.repo.Refs {
			case "empty":
				fmt.Fprint(rw, "[]")
			case "nonempty":
				fmt.Fprint(rw, `[{"ref":"refs/heads/main"}]`)
			case "incomplete":
				rw.Header().Set("Link", `<next>; rel="next"`)
				fmt.Fprint(rw, "[]")
			case "empty-409":
				rw.WriteHeader(409)
				fmt.Fprint(rw, `{"message":"Git Repository is empty."}`)
			}
		case strings.HasSuffix(p, "/branches") || strings.HasSuffix(p, "/tags"):
			fmt.Fprint(rw, "[]")
		default:
			t.Errorf("unexpected %s %s", r.Method, p)
		}
	}))
	t.Cleanup(srv.Close)
	base = srv.URL
	cfg := ParseConfig(map[string]Var{"YGG_HOST_1": {Value: "github " + srv.URL}, "YGG_HOST_1_TOKEN": {Value: "sentinel-secret"}})
	for _, in := range cfg.CoreInstances() {
		if in.Base == srv.URL {
			w.id = in.ID
		}
	}
	w.h = NewConfirmedHosts(New(Deps{Config: cfg}), nil, srv.Client())
	return w
}
func (w *httpRepoWorld) host() RepoHost                 { return w.h }
func (w *httpRepoWorld) instance() corehosts.InstanceID { return w.id }

func TestRepoHostContract(t *testing.T) {
	ctx := context.Background()
	worlds := map[string]func(t *testing.T) repoWorld{
		"fake":     func(*testing.T) repoWorld { return &fakeRepoHost{id: "git.acme.test", status: 201, reply: "ok"} },
		"recorded": func(t *testing.T) repoWorld { return newHTTPRepoWorld(t) },
	}
	good := &repoState{ID: 42, Owner: "acme", Private: true, Desc: repoDesc, Refs: "empty"}
	with := func(f func(r *repoState)) *repoState { c := *good; f(&c); return &c }
	scenarios := []struct {
		name string
		run  func(t *testing.T, w repoWorld)
	}{
		{"user owner may create only under their own name", func(t *testing.T, w repoWorld) {
			w.setOwner("user", true)
			if got, err := w.host().ResolveOwner(ctx, w.instance(), "acme"); err != nil || got != (RepoOwnerInfo{Kind: "user", CanCreate: true}) {
				t.Fatalf("%+v %v", got, err)
			}
			w.setOwner("user", false)
			if got, err := w.host().ResolveOwner(ctx, w.instance(), "acme"); err != nil || got.CanCreate {
				t.Fatalf("%+v %v", got, err)
			}
		}},
		{"org owner may create only as an active admin", func(t *testing.T, w repoWorld) {
			w.setOwner("org", true)
			if got, err := w.host().ResolveOwner(ctx, w.instance(), "acme"); err != nil || got != (RepoOwnerInfo{Kind: "org", CanCreate: true}) {
				t.Fatalf("%+v %v", got, err)
			}
			w.setOwner("org", false)
			if got, err := w.host().ResolveOwner(ctx, w.instance(), "acme"); err != nil || got.CanCreate {
				t.Fatalf("%+v %v", got, err)
			}
		}},
		{"create under a user: exactly one POST, identity returned", func(t *testing.T, w repoWorld) {
			w.setOwner("user", true)
			got, err := w.host().CreateRepo(ctx, repoSpec(w.instance(), "user"))
			if err != nil || got.RemoteID != "42" || !strings.HasSuffix(got.URL, "/acme/shop.git") || w.posts() != 1 {
				t.Fatalf("%+v %v posts %d", got, err, w.posts())
			}
		}},
		{"create under an org", func(t *testing.T, w repoWorld) {
			w.setOwner("org", true)
			if got, err := w.host().CreateRepo(ctx, repoSpec(w.instance(), "org")); err != nil || got.RemoteID != "42" || w.posts() != 1 {
				t.Fatalf("%+v %v", got, err)
			}
		}},
		{"no permission: no POST", func(t *testing.T, w repoWorld) {
			w.setOwner("user", false)
			if _, err := w.host().CreateRepo(ctx, repoSpec(w.instance(), "user")); err == nil || w.posts() != 0 {
				t.Fatalf("%v posts %d", err, w.posts())
			}
		}},
		{"the wrong owner kind: no POST", func(t *testing.T, w repoWorld) {
			w.setOwner("user", true)
			if _, err := w.host().CreateRepo(ctx, repoSpec(w.instance(), "org")); err == nil || w.posts() != 0 {
				t.Fatalf("%v posts %d", err, w.posts())
			}
		}},
		{"a lost reply is unknown after one POST", func(t *testing.T, w repoWorld) {
			w.setOwner("user", true)
			w.setPost(503, "")
			if _, err := w.host().CreateRepo(ctx, repoSpec(w.instance(), "user")); !errors.Is(err, ErrOutcomeUnknown) || w.posts() != 1 {
				t.Fatalf("%v posts %d", err, w.posts())
			}
		}},
		{"a malformed 2xx reply is unknown", func(t *testing.T, w repoWorld) {
			w.setOwner("user", true)
			w.setPost(201, "malformed")
			if _, err := w.host().CreateRepo(ctx, repoSpec(w.instance(), "user")); !errors.Is(err, ErrOutcomeUnknown) || w.posts() != 1 {
				t.Fatalf("%v posts %d", err, w.posts())
			}
		}},
		{"422 is a definite rejection, 403 a token problem", func(t *testing.T, w repoWorld) {
			w.setOwner("user", true)
			w.setPost(422, "")
			if _, err := w.host().CreateRepo(ctx, repoSpec(w.instance(), "user")); !errors.Is(err, ErrWriteRejected) {
				t.Fatal(err)
			}
			w.setPost(403, "")
			var fe *FetchError
			if _, err := w.host().CreateRepo(ctx, repoSpec(w.instance(), "user")); !errors.As(err, &fe) || fe.Kind != corehosts.FailUnauthorized {
				t.Fatal(err)
			}
		}},
		{"lookup: exact match with empty refs", func(t *testing.T, w repoWorld) {
			w.setRepo(good)
			if got, err := w.host().LookupRepo(ctx, repoSpec(w.instance(), "user")); err != nil || got.Outcome != RepoExactMatch || got.RemoteID != "42" || !strings.HasSuffix(got.URL, "/acme/shop.git") {
				t.Fatalf("%+v %v", got, err)
			}
		}},
		{"lookup: exact match when the host answers 409 and branches and tags are empty", func(t *testing.T, w repoWorld) {
			w.setRepo(with(func(r *repoState) { r.Refs = "empty-409" }))
			if got, err := w.host().LookupRepo(ctx, repoSpec(w.instance(), "user")); err != nil || got.Outcome != RepoExactMatch {
				t.Fatalf("%+v %v", got, err)
			}
		}},
		{"lookup: refs present is a conflict", func(t *testing.T, w repoWorld) {
			w.setRepo(with(func(r *repoState) { r.Refs = "nonempty" }))
			if got, _ := w.host().LookupRepo(ctx, repoSpec(w.instance(), "user")); got.Outcome != RepoConflict {
				t.Fatalf("%+v", got)
			}
		}},
		{"lookup: an incomplete ref list is unknown", func(t *testing.T, w repoWorld) {
			w.setRepo(with(func(r *repoState) { r.Refs = "incomplete" }))
			if got, _ := w.host().LookupRepo(ctx, repoSpec(w.instance(), "user")); got.Outcome != RepoUnknown {
				t.Fatalf("%+v", got)
			}
		}},
		{"lookup: an unrelated repository of that name is a conflict", func(t *testing.T, w repoWorld) {
			for name, r := range map[string]*repoState{
				"other owner":  with(func(r *repoState) { r.Owner = "mallory" }),
				"public":       with(func(r *repoState) { r.Private = false }),
				"no marker":    with(func(r *repoState) { r.Desc = "my own repository" }),
				"other marker": with(func(r *repoState) { r.Desc = "Created by Yggdrasil [ygg:bbbbbbbbbbbbbbbb]" }),
			} {
				w.setRepo(r)
				if got, _ := w.host().LookupRepo(ctx, repoSpec(w.instance(), "user")); got.Outcome != RepoConflict {
					t.Fatalf("%s adopted: %+v", name, got)
				}
			}
		}},
		{"lookup: a 404 is never proof of absence", func(t *testing.T, w repoWorld) {
			w.setRepo(nil)
			got, err := w.host().LookupRepo(ctx, repoSpec(w.instance(), "user"))
			if got.Outcome == RepoAbsent || got.Outcome == RepoExactMatch || err == nil {
				t.Fatalf("%+v %v", got, err)
			}
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			for name, mk := range worlds {
				t.Run(name, func(t *testing.T) { sc.run(t, mk(t)) })
			}
		})
	}
}
