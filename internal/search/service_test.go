package search_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coresearch "github.com/brendatamaar/yggdrasil/internal/core/search"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/search"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

type repoList []coresearch.RepoRef

func (l repoList) ActiveRepos(context.Context) ([]coresearch.RepoRef, error) { return l, nil }

func lit(p string) coresearch.Query {
	return coresearch.Query{Pattern: p, Mode: coresearch.ModeLiteral, Case: true}
}

func manyMatches(n int) search.GrepResult {
	var r search.GrepResult
	for i := 0; i < n; i++ {
		r.Matches = append(r.Matches, coresearch.Match{Path: fmt.Sprintf("f%d.txt", i), Line: 1, Col: 1, Text: "x"})
	}
	return r
}

func newSvc(t *testing.T, f *searchtest.Fake, repos repoList, mod func(*search.Deps)) *search.Service {
	t.Helper()
	d := search.Deps{Repos: repos, Grep: f}
	if mod != nil {
		mod(&d)
	}
	s, err := search.NewService(d)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func refs(f *searchtest.Fake, n int, files map[string]string) repoList {
	var out repoList
	for i := 0; i < n; i++ {
		out = append(out, coresearch.RepoRef{ID: core.RepoID(fmt.Sprintf("rep_%02d", i)), Name: fmt.Sprintf("shop-%02d", i), Path: f.Repo(files)})
	}
	return out
}

func TestSearchMergesAllRepos(t *testing.T) {
	f := searchtest.NewFake()
	repos := refs(f, 3, map[string]string{"a.txt": "needle\n"})
	st, err := newSvc(t, f, repos, nil).Search(context.Background(), lit("needle"))
	if err != nil || !st.Done() || len(st.Results) != 3 || len(st.Problems) != 0 || st.Truncated {
		t.Fatalf("state = %+v, err = %v", st, err)
	}
}

func TestSearchRefusesUntrackedRepoIDs(t *testing.T) {
	f := searchtest.NewFake()
	tracked := refs(f, 1, map[string]string{"a.txt": "needle\n"})
	// An untracked repo that contains the pattern exists, but is not in the
	// source of active repos (S3).
	f.Repo(map[string]string{"secret.txt": "needle\n"})
	s := newSvc(t, f, tracked, nil)

	q := lit("needle")
	q.Repos = []core.RepoID{"rep_untracked"}
	_, err := s.Search(context.Background(), q)
	if class, ok := apperr.ClassOf(err); !ok || class != apperr.Invalid {
		t.Fatalf("err = %v, want an invalid-request error", err)
	}
	if n := f.Calls.Load(); n != 0 {
		t.Fatalf("grep ran %d times for a refused request", n)
	}
	// An id plus a tracked one: still refused as a whole.
	q.Repos = []core.RepoID{tracked[0].ID, "rep_untracked"}
	if _, err := s.Search(context.Background(), q); err == nil {
		t.Fatal("a mixed request was accepted")
	}
	// No ids: only the tracked repo is searched.
	st, err := s.Search(context.Background(), lit("needle"))
	if err != nil || len(st.Results) != 1 || f.Calls.Load() != 1 {
		t.Fatalf("results = %+v, calls = %d, err = %v", st.Results, f.Calls.Load(), err)
	}
}

func TestSearchIsLimitedToUntrackedFolderWithRealGit(t *testing.T) {
	env := searchtest.GitEnv(t)
	g, err := search.NewGitGrepper(env, platform.Start)
	if err != nil {
		t.Fatal(err)
	}
	tracked := searchtest.InitRepo(t, env, map[string]string{"a.txt": "needle in tracked\n"})
	untracked := searchtest.InitRepo(t, env, map[string]string{"b.txt": "needle in untracked\n"})
	repos := repoList{{ID: "rep_a", Name: "shop-api", Path: tracked}}
	s, err := search.NewService(search.Deps{Repos: repos, Grep: g})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Search(context.Background(), lit("needle"))
	if err != nil || len(st.Results) != 1 || st.Results[0].Path != "a.txt" {
		t.Fatalf("results = %+v, err = %v", st.Results, err)
	}
	q := lit("needle")
	q.Repos = []core.RepoID{"rep_b"}
	if _, err := s.Search(context.Background(), q); err == nil {
		t.Fatal("the untracked repo id was searched")
	}
	_ = untracked
}

func TestSearchNoReposIsDoneAndEmpty(t *testing.T) {
	f := searchtest.NewFake()
	st, err := newSvc(t, f, nil, nil).Search(context.Background(), lit("x"))
	if err != nil || !st.Done() || len(st.Results) != 0 {
		t.Fatalf("state = %+v, err = %v", st, err)
	}
}

func TestSearchRechecksTheQuery(t *testing.T) {
	f := searchtest.NewFake()
	s := newSvc(t, f, refs(f, 1, nil), nil)
	for _, q := range []coresearch.Query{
		{Pattern: "", Mode: coresearch.ModeLiteral},
		{Pattern: "(", Mode: coresearch.ModeRegex},
		{Pattern: "a", Mode: coresearch.ModeLiteral, Paths: []string{"../x"}},
	} {
		if _, err := s.Search(context.Background(), q); err == nil {
			t.Errorf("query %+v was accepted", q)
		}
	}
	if f.Calls.Load() != 0 {
		t.Fatal("grep ran for an invalid query")
	}
}

func TestSearchRecordsMissingAndFailedRepos(t *testing.T) {
	f := searchtest.NewFake()
	repos := refs(f, 3, map[string]string{"a.txt": "needle\n"})
	repos[0].Path = "gone-folder"
	f.Script(repos[1].Path, func(context.Context) (search.GrepResult, error) {
		return search.GrepResult{}, fmt.Errorf("%w: boom", search.ErrGitFailed)
	})
	st, err := newSvc(t, f, repos, nil).Search(context.Background(), lit("needle"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Problems[repos[0].ID] != coresearch.ProblemMissing || st.Problems[repos[1].ID] != coresearch.ProblemGitFailed || len(st.Results) != 1 {
		t.Fatalf("problems = %v, results = %d", st.Problems, len(st.Results))
	}
}

func TestSearchPerRepoTimeoutAndDeadline(t *testing.T) {
	f := searchtest.NewFake()
	repos := refs(f, 2, map[string]string{"a.txt": "needle\n"})
	block := func(ctx context.Context) (search.GrepResult, error) {
		<-ctx.Done()
		return search.GrepResult{}, ctx.Err()
	}
	f.Script(repos[0].Path, block)
	// One slow repo times out, the other answers.
	s := newSvc(t, f, repos, func(d *search.Deps) { d.PerRepoTimeout = 30 * time.Millisecond })
	st, err := s.Search(context.Background(), lit("needle"))
	if err != nil || st.Problems[repos[0].ID] != coresearch.ProblemTimedOut || len(st.Results) != 1 {
		t.Fatalf("per-repo timeout: problems = %v, results = %d, err = %v", st.Problems, len(st.Results), err)
	}
	// The overall deadline times out whatever is still pending.
	f.Script(repos[1].Path, block)
	s = newSvc(t, f, repos, func(d *search.Deps) { d.Deadline = 30 * time.Millisecond; d.PerRepoTimeout = time.Minute })
	st, err = s.Search(context.Background(), lit("needle"))
	if err != nil || !st.Done() || len(st.Problems) != 2 || st.Problems[repos[1].ID] != coresearch.ProblemTimedOut {
		t.Fatalf("deadline: problems = %v, err = %v", st.Problems, err)
	}
}

func TestSearchClientGoneStopsEveryWorker(t *testing.T) {
	f := searchtest.NewFake()
	repos := refs(f, 12, nil)
	for _, r := range repos {
		f.Script(r.Path, func(ctx context.Context) (search.GrepResult, error) {
			<-ctx.Done()
			return search.GrepResult{}, ctx.Err()
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := newSvc(t, f, repos, nil).Search(ctx, lit("x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if f.Active.Load() != 0 {
		t.Fatalf("%d workers still active", f.Active.Load())
	}
}

func TestSearchRunsAtMostEightGreps(t *testing.T) {
	f := searchtest.NewFake()
	repos := refs(f, 30, nil)
	for _, r := range repos {
		f.Script(r.Path, func(ctx context.Context) (search.GrepResult, error) {
			select {
			case <-time.After(5 * time.Millisecond):
			case <-ctx.Done():
			}
			return search.GrepResult{}, nil
		})
	}
	st, err := newSvc(t, f, repos, nil).Search(context.Background(), lit("x"))
	if err != nil || !st.Done() {
		t.Fatalf("state = %+v, err = %v", st, err)
	}
	if got := f.MaxActive.Load(); got > search.DefaultParallel || got < 2 {
		t.Fatalf("max concurrent greps = %d, want 2..%d", got, search.DefaultParallel)
	}
}

func TestSearchCapsTotalMatches(t *testing.T) {
	f := searchtest.NewFake()
	repos := refs(f, 7, nil)
	for _, r := range repos {
		f.Script(r.Path, func(context.Context) (search.GrepResult, error) { return manyMatches(coresearch.MaxPerRepo), nil })
	}
	st, err := newSvc(t, f, repos, nil).Search(context.Background(), lit("x"))
	if err != nil || len(st.Results) != coresearch.MaxTotal || !st.Truncated {
		t.Fatalf("results = %d, truncated = %v, err = %v", len(st.Results), st.Truncated, err)
	}
}

func TestEditorSetting(t *testing.T) {
	f := searchtest.NewFake()
	for saved, want := range map[string]coresearch.Editor{"vscode": coresearch.EditorVSCode, "idea": coresearch.EditorIdea, "": "", "emacs": ""} {
		s := newSvc(t, f, nil, func(d *search.Deps) { d.Editor = func(context.Context) string { return saved } })
		if got := s.Editor(context.Background()); got != want {
			t.Errorf("editor %q = %q, want %q", saved, got, want)
		}
	}
	if got := newSvc(t, f, nil, nil).Editor(context.Background()); got != "" {
		t.Errorf("no setting = %q", got)
	}
}

func TestNewServiceNeedsItsParts(t *testing.T) {
	if _, err := search.NewService(search.Deps{}); err == nil {
		t.Fatal("an empty Deps was accepted")
	}
	_ = filepath.Join
}
