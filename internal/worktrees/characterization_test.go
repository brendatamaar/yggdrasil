package worktrees_test

// Characterization tests for worktrees.Ensurer (WE-04): they pin the
// behaviour that the extraction into internal/core/worktrees must keep.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
	"github.com/brendatamaar/yggdrasil/internal/worktrees/worktreestest"
)

// spyGit records every call and can fail WorktreeAdd.
type spyGit struct {
	*worktreestest.Git
	mu       sync.Mutex
	probes   []string // branch names asked about, in order
	resolved []string // refs passed to ResolveCommit
	addCalls int
	addErr   error
}

func (g *spyGit) BranchExists(ctx context.Context, dir, name string) (bool, bool, error) {
	g.mu.Lock()
	g.probes = append(g.probes, name)
	g.mu.Unlock()
	return g.Git.BranchExists(ctx, dir, name)
}

func (g *spyGit) ResolveCommit(ctx context.Context, dir, ref string) (string, error) {
	g.mu.Lock()
	g.resolved = append(g.resolved, ref)
	g.mu.Unlock()
	return g.Git.ResolveCommit(ctx, dir, ref)
}

func (g *spyGit) WorktreeAdd(ctx context.Context, repo, path, branch, start string) (git.Worktree, error) {
	g.mu.Lock()
	g.addCalls++
	err := g.addErr
	g.mu.Unlock()
	if err != nil {
		return git.Worktree{}, err
	}
	return g.Git.WorktreeAdd(ctx, repo, path, branch, start)
}

// spyStore records Record calls and can fail them.
type spyStore struct {
	*worktreestest.Store
	records []recorded
	fail    error
}

type recorded struct {
	W     core.Worktree
	Base  string
	Audit core.AuditEntry
}

func (s *spyStore) Record(ctx context.Context, w core.Worktree, base string, a core.AuditEntry) error {
	if s.fail != nil {
		return s.fail
	}
	if err := s.Store.Record(ctx, w, base, a); err != nil {
		return err
	}
	s.records = append(s.records, recorded{w, base, a})
	return nil
}

type charEnv struct {
	e    *worktrees.Ensurer
	g    *spyGit
	s    *spyStore
	data string
	now  time.Time
}

func newCharEnv(t *testing.T) *charEnv {
	t.Helper()
	g := &spyGit{Git: worktreestest.NewGit()}
	s := &spyStore{Store: worktreestest.NewStore()}
	data := filepath.Join(t.TempDir(), "data")
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	e, err := worktrees.New(worktrees.Config{Git: g, Store: s, Rand: &worktreestest.Rand{}, Now: func() time.Time { return now }, NewID: newID, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	return &charEnv{e: e, g: g, s: s, data: data, now: now}
}

func repoRef(t *testing.T, id string) worktrees.RepoRef {
	return worktrees.RepoRef{ID: core.RepoID(id), Path: filepath.Join(t.TempDir(), "my repo"), DefaultBranch: "main"}
}

func TestCharEachRetryUsesANewNameAndProbesOnce(t *testing.T) {
	env := newCharEnv(t)
	r := repoRef(t, "rep_1")
	scripted := &worktreestest.Rand{}
	var want []worktrees.Name
	for i := range 4 {
		n := worktrees.Generate(scripted, i)
		want = append(want, n)
		env.g.AddBranch(r.Path, n.Branch())
	}
	got, err := env.e.Ensure(context.Background(), "tsk_1", []worktrees.RepoRef{r})
	if err != nil {
		t.Fatal(err)
	}
	fifth := worktrees.Generate(scripted, 4)
	if got[0].Name != string(fifth) {
		t.Fatalf("name %q, want the fifth candidate %q", got[0].Name, fifth)
	}
	if len(env.g.probes) != 5 {
		t.Fatalf("%d probes, want 5: %v", len(env.g.probes), env.g.probes)
	}
	seen := map[string]bool{}
	for _, p := range env.g.probes {
		if seen[p] {
			t.Fatalf("branch %q probed twice: %v", p, env.g.probes)
		}
		seen[p] = true
	}
	for i, n := range want {
		if env.g.probes[i] != n.Branch() {
			t.Fatalf("probe %d = %q, want %q", i, env.g.probes[i], n.Branch())
		}
	}
	if env.g.addCalls != 1 {
		t.Fatalf("%d worktree adds, want 1", env.g.addCalls)
	}
}

func TestCharExhaustedNamesProbeExactlyFiveAndAddNothing(t *testing.T) {
	env := newCharEnv(t)
	r := repoRef(t, "rep_1")
	scripted := &worktreestest.Rand{}
	for i := range 5 {
		env.g.AddBranch(r.Path, worktrees.Generate(scripted, i).Branch())
	}
	got, err := env.e.Ensure(context.Background(), "tsk_1", []worktrees.RepoRef{r})
	var we *worktrees.Error
	if !errors.Is(err, worktrees.ErrNameTaken) || !errors.As(err, &we) || we.UserMessage() == "" || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(env.g.probes) != 5 || env.g.addCalls != 0 || len(env.s.records) != 0 {
		t.Fatalf("probes %d, adds %d, records %d; want 5, 0, 0", len(env.g.probes), env.g.addCalls, len(env.s.records))
	}
}

func TestCharAuditIsWrittenWithTheRowInOneRecordCall(t *testing.T) {
	env := newCharEnv(t)
	r := repoRef(t, "rep_1")
	got, err := env.e.Ensure(context.Background(), "tsk_9", []worktrees.RepoRef{r})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.s.records) != 1 || len(env.s.Audits) != 1 {
		t.Fatalf("%d records, %d audits, want 1 and 1", len(env.s.records), len(env.s.Audits))
	}
	rec := env.s.records[0]
	if rec.W.ID != got[0].ID || rec.Base != worktreestest.FakeCommit {
		t.Fatalf("recorded %+v", rec)
	}
	a := rec.Audit
	if a.Actor != "system" || a.Action != "worktree.create" || a.Subject != "task:tsk_9" || !a.At.Equal(env.now) || !rec.W.CreatedAt.Equal(env.now) {
		t.Fatalf("audit %+v", a)
	}
	var d map[string]string
	if err := json.Unmarshal(a.Detail, &d); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"worktree": string(got[0].ID), "repo": "rep_1", "branch": got[0].Branch, "base": worktreestest.FakeCommit}
	if len(d) != len(want) {
		t.Fatalf("detail %v", d)
	}
	for k, v := range want {
		if d[k] != v {
			t.Fatalf("detail[%s] = %q, want %q", k, d[k], v)
		}
	}
	if strings.Contains(string(a.Detail), got[0].Path) {
		t.Fatal("audit detail holds a path")
	}
}

func TestCharRecordFailureKeepsEarlierAndExplains(t *testing.T) {
	env := newCharEnv(t)
	a, b := repoRef(t, "rep_a"), repoRef(t, "rep_b")
	first, err := env.e.Ensure(context.Background(), "tsk_1", []worktrees.RepoRef{a})
	if err != nil {
		t.Fatal(err)
	}
	env.s.fail = errors.New("disk full")
	got, err := env.e.Ensure(context.Background(), "tsk_1", []worktrees.RepoRef{a, b})
	var we *worktrees.Error
	if err == nil || !errors.As(err, &we) || we.UserMessage() == "" {
		t.Fatalf("want a plain-language error, got %v", err)
	}
	if len(got) != 1 || got[0].ID != first[0].ID {
		t.Fatalf("got %+v, want the reused first worktree only", got)
	}
	if _, err := os.Stat(first[0].Path); err != nil {
		t.Fatalf("earlier worktree folder gone: %v", err)
	}
}

func TestCharGitAddFailureIsNotRetried(t *testing.T) {
	env := newCharEnv(t)
	env.g.addErr = errors.New("git: boom")
	got, err := env.e.Ensure(context.Background(), "tsk_1", []worktrees.RepoRef{repoRef(t, "rep_1")})
	var we *worktrees.Error
	if err == nil || !errors.As(err, &we) || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if env.g.addCalls != 1 || len(env.s.records) != 0 {
		t.Fatalf("adds %d, records %d; want 1 and 0", env.g.addCalls, len(env.s.records))
	}
}

func TestCharCancelledContextReturnsContextError(t *testing.T) {
	env := newCharEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env.g.addErr = errors.New("killed")
	_, err := env.e.Ensure(ctx, "tsk_1", []worktrees.RepoRef{repoRef(t, "rep_1")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	var we *worktrees.Error
	if errors.As(err, &we) {
		t.Fatalf("a cancel must not read as a git failure: %v", err)
	}
}

func TestCharOnlyActiveRowsAreReused(t *testing.T) {
	env := newCharEnv(t)
	ctx := context.Background()
	r := repoRef(t, "rep_1")
	for i, st := range []core.WorktreeStatus{core.WorktreeMerged, core.WorktreeRemoved} {
		id := core.WorktreeID("wt_old" + string(rune('a'+i)))
		old := core.Worktree{ID: id, RepoID: r.ID, TaskID: "tsk_1", Name: "calm-old", Path: filepath.Join(env.data, "wt", "calm-old"),
			Branch: "ygg/calm-old", Status: st, CreatedAt: env.now}
		if err := env.s.Record(ctx, old, "abc", core.AuditEntry{ID: core.AuditID("aud_old" + string(rune('a'+i))), At: env.now, Actor: "system", Action: "worktree.create", Subject: "task:tsk_1"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := env.e.Ensure(ctx, "tsk_1", []worktrees.RepoRef{r})
	if err != nil || len(got) != 1 || got[0].Status != core.WorktreeActive || strings.HasPrefix(string(got[0].ID), "wt_old") {
		t.Fatalf("got %+v, %v; want a new active worktree", got, err)
	}
}

func TestCharResultsFollowRequestOrderAndPassDefaultBranch(t *testing.T) {
	env := newCharEnv(t)
	repos := []worktrees.RepoRef{repoRef(t, "rep_z"), repoRef(t, "rep_a"), repoRef(t, "rep_m")}
	repos[1].DefaultBranch = "develop"
	repos[2].DefaultBranch = ""
	got, err := env.e.Ensure(context.Background(), "tsk_1", repos)
	if err != nil || len(got) != 3 {
		t.Fatalf("got %+v, %v", got, err)
	}
	for i, w := range got {
		if w.RepoID != repos[i].ID {
			t.Fatalf("result %d is for %s, want %s", i, w.RepoID, repos[i].ID)
		}
	}
	if strings.Join(env.g.resolved, ",") != "main,develop," {
		t.Fatalf("resolved refs %q", env.g.resolved)
	}
}

func TestCharBadTaskIDIsRejectedBeforeAnyWork(t *testing.T) {
	env := newCharEnv(t)
	got, err := env.e.Ensure(context.Background(), "", []worktrees.RepoRef{repoRef(t, "rep_1")})
	if err == nil || len(got) != 0 || len(env.g.probes) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}
