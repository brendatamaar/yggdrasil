package repos_test

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

type refreshCall struct {
	ids    []core.RepoID
	reason corerepos.RefreshReason
}

type refreshRecorder struct {
	mu      sync.Mutex
	calls   []refreshCall
	reloads int
}

func (r *refreshRecorder) ReloadRepos(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloads++
	return nil
}

func (r *refreshRecorder) reloadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reloads
}

func (r *refreshRecorder) Refresh(_ context.Context, ids []core.RepoID, reason corerepos.RefreshReason) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, refreshCall{ids: append([]core.RepoID(nil), ids...), reason: reason})
	return nil
}

type env struct {
	t     *testing.T
	svc   *repos.TrackingService
	store *db.Store
	rec   *refreshRecorder
	root  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	red := safety.NewRedactor()
	store := db.NewStore(d, red.Redact)
	rec := &refreshRecorder{}
	clock := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	svc, err := repos.NewTrackingService(repos.Deps{
		Store: store, Redactor: red, Status: rec,
		Clock: func() time.Time { clock = clock.Add(time.Minute); return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return &env{t: t, svc: svc, store: store, rec: rec, root: root}
}

// repoDir makes a folder with a .git folder and a file in it.
func (e *env) repoDir(rel string) {
	e.t.Helper()
	dir := filepath.Join(e.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\r\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func pick(rel string) corerepos.Pick { return corerepos.Pick{RelPath: corerepos.RelPath(rel)} }

func (e *env) track(name string, rels ...string) corerepos.Outcome {
	e.t.Helper()
	var ps []corerepos.Pick
	for _, r := range rels {
		ps = append(ps, pick(r))
	}
	out, err := e.svc.Track(context.Background(), e.root, name, ps, "user")
	if err != nil {
		e.t.Fatalf("Track: %v", err)
	}
	return out
}

func (e *env) audits(subject string) []string {
	e.t.Helper()
	list, err := e.store.ListAudit(context.Background(), subject, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	var actions []string
	for _, a := range list {
		actions = append(actions, a.Action)
	}
	sort.Strings(actions)
	return actions
}

// treeHash fingerprints every file and folder name and every file's content.
func treeHash(t *testing.T, root string) [32]byte {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		h.Write([]byte(p + "\x00"))
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func TestTrackUntrackRestoreLifecycle(t *testing.T) {
	e := newEnv(t)
	e.repoDir("shop-api")
	e.repoDir("libs/shop-web")
	before := treeHash(t, e.root) // S2: tracking never touches the files
	ctx := context.Background()

	out := e.track("acme-shop", "shop-api", "libs/shop-web")
	tracked, ok := out.(corerepos.Tracked)
	if !ok || len(tracked.Added) != 2 || len(tracked.Restored) != 0 {
		t.Fatalf("outcome = %#v", out)
	}
	if got := e.audits("project:" + string(tracked.ProjectID)); !reflect.DeepEqual(got, []string{"project.track"}) {
		t.Errorf("project audit = %v", got)
	}
	for _, id := range tracked.Added {
		if got := e.audits("repo:" + string(id)); !reflect.DeepEqual(got, []string{"repo.track"}) {
			t.Errorf("repo audit = %v", got)
		}
	}
	if len(e.rec.calls) != 1 || e.rec.calls[0].reason != corerepos.ReasonTracking || len(e.rec.calls[0].ids) != 2 {
		t.Errorf("refresh calls = %+v", e.rec.calls)
	}
	projects, err := e.svc.Projects(ctx)
	if err != nil || len(projects) != 1 || len(projects[0].Repos) != 2 {
		t.Fatalf("Projects = %+v, %v", projects, err)
	}
	apiID := tracked.Added[0]
	r, err := e.store.GetRepo(ctx, apiID)
	if err != nil || !strings.HasPrefix(r.Path, e.root[:2]) {
		t.Fatalf("GetRepo = %+v, %v", r, err)
	}

	// Tracking again is a no-op: no rows, no audit, no refresh.
	if out := e.track("acme-shop", "shop-api"); !reflect.DeepEqual(out, corerepos.AlreadyTracked{ProjectID: tracked.ProjectID}) {
		t.Errorf("second track = %#v", out)
	}
	if len(e.rec.calls) != 1 {
		t.Errorf("a no-op refreshed status: %+v", e.rec.calls)
	}

	// Trust a repo, untrack it: invisible, audited.
	if err := e.store.Commit(ctx, func(tx *db.Tx) error { return tx.SetRepoTrusted(ctx, apiID, true) }); err != nil {
		t.Fatal(err)
	}
	reloadsBefore := e.rec.reloadCount()
	un, err := e.svc.UntrackRepo(ctx, apiID, "user")
	if err != nil || len(un.(corerepos.UntrackDone).Repos) != 1 {
		t.Fatalf("UntrackRepo = %#v, %v", un, err)
	}
	// The status service drops the untracked repo from its counts.
	if e.rec.reloadCount() != reloadsBefore+1 {
		t.Errorf("untrack reloaded the status %d times, want 1", e.rec.reloadCount()-reloadsBefore)
	}
	if _, err := e.store.GetRepo(ctx, apiID); err == nil {
		t.Error("an untracked repo is still readable by GetRepo")
	}
	if got := e.audits("repo:" + string(apiID)); !reflect.DeepEqual(got, []string{"repo.track", "repo.untrack"}) {
		t.Errorf("audit = %v", got)
	}
	// Idempotent: nothing more is written.
	if _, err := e.svc.UntrackRepo(ctx, apiID, "user"); err != nil {
		t.Fatal(err)
	}
	if got := e.audits("repo:" + string(apiID)); len(got) != 2 {
		t.Errorf("a repeated untrack was audited: %v", got)
	}

	// Track it again: same ID, history kept, not trusted.
	out = e.track("acme-shop", "shop-api")
	if got := out.(corerepos.Tracked); !reflect.DeepEqual(got.Restored, []core.RepoID{apiID}) || len(got.Added) != 0 {
		t.Errorf("restore outcome = %#v", out)
	}
	if r, err := e.store.GetRepo(ctx, apiID); err != nil || r.Trusted {
		t.Errorf("restored repo = %+v, %v; want visible and not trusted", r, err)
	}

	// Untrack the project, then track the folder again: the project returns.
	if _, err := e.svc.UntrackProject(ctx, tracked.ProjectID, "user"); err != nil {
		t.Fatal(err)
	}
	if ps, _ := e.svc.Projects(ctx); len(ps) != 0 {
		t.Errorf("Projects after untrack = %+v", ps)
	}
	out = e.track("acme-shop-2", "shop-api")
	if got := out.(corerepos.Tracked); got.ProjectID != tracked.ProjectID || len(got.Restored) != 1 {
		t.Errorf("project restore = %#v", out)
	}
	ps, _ := e.svc.Projects(ctx)
	if len(ps) != 1 || ps[0].Name != "acme-shop-2" || len(ps[0].Repos) != 1 {
		t.Errorf("Projects = %+v", ps)
	}
	if got := e.audits("project:" + string(tracked.ProjectID)); !reflect.DeepEqual(got, []string{"project.restore", "project.track", "project.untrack"}) {
		t.Errorf("project audit = %v", got)
	}

	if after := treeHash(t, e.root); after != before {
		t.Error("tracking, untracking and restoring changed files on disk")
	}
}

func TestTrackRejections(t *testing.T) {
	e := newEnv(t)
	e.repoDir("a")
	e.repoDir("b")
	ctx := context.Background()
	out := e.track("one", "a").(corerepos.Tracked)

	// Not a repo, not a folder, escaping: refused before anything is written.
	if err := os.MkdirAll(filepath.Join(e.root, "plain"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, rel := range map[string]string{"not a repo": "plain", "missing": "ghost", "dotdot": "../x"} {
		_, err := e.svc.TrackRepos(ctx, out.ProjectID, []corerepos.Pick{pick(rel)}, "user")
		wantInvalid(t, name, err)
	}

	// Another project over the same folder, or inside it.
	other := t.TempDir()
	if _, err := e.svc.Track(ctx, other, "one", []corerepos.Pick{pick(".")}, "user"); err == nil {
		t.Error("tracking a folder that isn't a repo was accepted")
	}
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.Track(ctx, other, "one", []corerepos.Pick{pick(".")}, "user"); err != nil || !reflect.DeepEqual(got, corerepos.NameTaken{Name: "one"}) {
		t.Errorf("name clash = %#v, %v", got, err)
	}
	inner := filepath.Join(e.root, "a")
	if got, err := e.svc.Track(ctx, inner, "inner", []corerepos.Pick{pick(".")}, "user"); err != nil || !reflect.DeepEqual(got, corerepos.Overlaps{Other: out.ProjectID}) {
		t.Errorf("overlap = %#v, %v", got, err)
	}
	if got, err := e.svc.RenameProject(ctx, out.ProjectID, "  renamed ", "user"); err != nil || !reflect.DeepEqual(got, corerepos.Renamed{ProjectID: out.ProjectID}) {
		t.Errorf("rename = %#v, %v", got, err)
	}
	if got, err := e.svc.UntrackRepo(ctx, "rep_unknown", "user"); err != nil || !reflect.DeepEqual(got, corerepos.NotFound{}) {
		t.Errorf("unknown repo = %#v, %v", got, err)
	}
	if _, err := e.svc.RenameProject(ctx, out.ProjectID, " ", "user"); err == nil {
		t.Error("a blank name was accepted")
	}
	if _, err := e.svc.Rescan(ctx, "prj_unknown"); err == nil {
		t.Error("rescan of an unknown project")
	} else if c, _ := apperr.ClassOf(err); c != apperr.NotFound {
		t.Errorf("rescan error class = %v", c)
	}
}

func TestPreviewAndRescan(t *testing.T) {
	e := newEnv(t)
	e.repoDir("a")
	e.repoDir("b")
	ctx := context.Background()

	pv, err := e.svc.Preview(ctx, e.root)
	if err != nil || len(pv.Candidates) != 2 || pv.Existing != nil || pv.Candidates[0].Known != nil {
		t.Fatalf("Preview = %+v, %v", pv, err)
	}
	out := e.track("one", "a").(corerepos.Tracked)
	pv, _ = e.svc.Preview(ctx, e.root)
	if pv.Existing == nil || *pv.Existing != out.ProjectID {
		t.Fatalf("Existing = %v", pv.Existing)
	}
	if a, b := pv.Candidates[0], pv.Candidates[1]; a.RelPath != "a" || a.Known == nil || !a.Active || b.Known != nil {
		t.Errorf("candidates = %+v", pv.Candidates)
	}

	rep, err := e.svc.Rescan(ctx, out.ProjectID)
	if err != nil || len(rep.New) != 1 || rep.New[0].RelPath != "b" || len(rep.Missing) != 0 {
		t.Fatalf("Rescan = %+v, %v", rep, err)
	}
	// The tracked repo loses its .git: rescan reports it, never untracks it.
	if err := os.RemoveAll(filepath.Join(e.root, "a", ".git")); err != nil {
		t.Fatal(err)
	}
	rep, _ = e.svc.Rescan(ctx, out.ProjectID)
	if len(rep.Missing) != 1 || rep.Missing[0] != out.Added[0] {
		t.Errorf("Missing = %v", rep.Missing)
	}
	if ps, _ := e.svc.Projects(ctx); len(ps) != 1 || len(ps[0].Repos) != 1 {
		t.Error("rescan changed what is tracked")
	}
	if _, err := e.svc.TrackRepos(ctx, out.ProjectID, []corerepos.Pick{pick("b")}, "user"); err != nil {
		t.Fatal(err)
	}
}

func TestBrowseDirs(t *testing.T) {
	e := newEnv(t)
	e.repoDir("repo")
	if err := os.MkdirAll(filepath.Join(e.root, "Zed"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.root, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	got, err := e.svc.BrowseDirs(ctx, e.root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range got.Entries {
		names = append(names, d.Name)
		if d.Name == "repo" && !d.IsRepo {
			t.Error("repo not flagged")
		}
	}
	if !reflect.DeepEqual(names, []string{"repo", "Zed"}) {
		t.Errorf("entries = %v, want folders only, sorted without case", names)
	}
	if got.Parent == "" || got.Path == "" {
		t.Errorf("listing = %+v", got)
	}
	roots, err := e.svc.BrowseDirs(ctx, "")
	if err != nil || len(roots.Entries) == 0 || roots.Parent != "" || roots.Path != "" {
		t.Errorf("roots = %+v, %v", roots, err)
	}
	if _, err := e.svc.BrowseDirs(ctx, filepath.Join(e.root, "nope")); err == nil {
		t.Error("a missing folder was listed")
	}
}

func TestNilStatusRefresherIsFine(t *testing.T) {
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	red := safety.NewRedactor()
	svc, err := repos.NewTrackingService(repos.Deps{Store: db.NewStore(d, red.Redact), Redactor: red})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Track(context.Background(), root, "solo", []corerepos.Pick{pick(".")}, "user")
	if err != nil || len(out.(corerepos.Tracked).Added) != 1 {
		t.Fatalf("Track = %#v, %v", out, err)
	}
}

func TestNewTrackingServiceNeedsStoreAndRedactor(t *testing.T) {
	if _, err := repos.NewTrackingService(repos.Deps{}); err == nil {
		t.Error("no store accepted")
	}
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := repos.NewTrackingService(repos.Deps{Store: db.NewStore(d, nil)}); err == nil {
		t.Error("no redactor accepted")
	}
}
