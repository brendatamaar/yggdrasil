package repos_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// Immutable fake data makes concurrent calls independent. The optional callback
// is used only by sequential scope/failure scenarios.
type historyGitFake struct {
	records []git.Commit
	partial bool
	err     error
	raw     bool
	after   func()
	calls   int
}

func (f *historyGitFake) LogRange(ctx context.Context, _ string, from, to time.Time, max int) ([]git.Commit, bool, error) {
	f.calls++
	if f.after != nil {
		f.after()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if f.err != nil {
		return nil, false, f.err
	}
	if f.raw {
		return slices.Clone(f.records), f.partial, nil
	}
	var cs []git.Commit
	for _, c := range f.records {
		if !c.Date.Before(from) && c.Date.Before(to) {
			cs = append(cs, c)
		}
	}
	slices.SortFunc(cs, func(a, b git.Commit) int {
		if a.Date.Equal(b.Date) {
			return strings.Compare(a.Hash, b.Hash)
		}
		return b.Date.Compare(a.Date)
	})
	partial := f.partial || len(cs) > max
	if len(cs) > max {
		cs = cs[:max]
	}
	return cs, partial, nil
}

var historyFrom = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func historyRows() []git.Commit {
	var cs []git.Commit
	for i, offset := range []int{-1, 0, 12, 12, 24} {
		cs = append(cs, git.Commit{Hash: fmt.Sprintf("%040x", i+1), Subject: fmt.Sprintf("change %d", i), Author: "Ada Test", Date: historyFrom.Add(time.Duration(offset) * time.Hour)})
	}
	return cs
}

// Consumer contract: identical bounds, author-time order, count truncation,
// empty history and cancellation clauses against fake and actual git.
func TestHistoryReaderContract(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprint("git=", live), func(t *testing.T) {
			src := &memSource{}
			row := repoRow("api")
			var g repos.HistoryGit = &historyGitFake{records: historyRows()}
			if live {
				fx := newGitFx(t)
				row.Path = fx.repo("shop-api")
				for _, c := range historyRows() {
					fx.run(row.Path, "commit", "-q", "--allow-empty", "-m", c.Subject, "--date="+c.Date.Format(time.RFC3339))
				}
				g = fx.g
			}
			src.set(row)
			reader := repos.NewHistoryReader(src, g)
			ctx := context.Background()
			to := historyFrom.Add(24 * time.Hour)
			cs, partial, err := reader.Commits(ctx, row.ID, historyFrom, to, 200)
			if err != nil || partial || len(cs) != 3 {
				t.Fatalf("commits=%+v partial=%v err=%v", cs, partial, err)
			}
			if cs[0].At.Before(cs[1].At) || cs[1].At.Before(cs[2].At) || cs[0].OID >= cs[1].OID {
				t.Fatal("author-date/hash order", cs)
			}
			for _, c := range cs {
				if c.At.Before(historyFrom) || !c.At.Before(to) || c.At.Location() != time.UTC {
					t.Fatal("range/UTC", c)
				}
			}
			capped, partial, err := reader.Commits(ctx, row.ID, historyFrom, to, 1)
			if err != nil || !partial || len(capped) != 1 || capped[0] != cs[0] {
				t.Fatal("count cutoff", capped, partial, err)
			}
			empty, partial, err := reader.Commits(ctx, row.ID, to, to.Add(time.Hour), 200)
			if err != nil || partial || len(empty) != 1 {
				t.Fatal("half-open boundary", empty, partial, err)
			}
			empty, partial, err = reader.Commits(ctx, row.ID, to.Add(time.Hour), to.Add(2*time.Hour), 200)
			if err != nil || partial || len(empty) != 0 {
				t.Fatal("empty history", empty, partial, err)
			}
			cancelled, stop := context.WithCancel(ctx)
			stop()
			empty, partial, err = reader.Commits(cancelled, row.ID, historyFrom, to, 200)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, repos.ErrHistoryUnavailable) || len(empty) != 0 || partial {
				t.Fatal("cancel unavailable", empty, partial, err)
			}
			src.set()
			if _, _, err := reader.Commits(ctx, row.ID, historyFrom, to, 200); !errors.Is(err, repos.ErrHistoryUnavailable) {
				t.Fatal("untracked", err)
			}
		})
	}
}

func TestHistoryPartialZeroAndErrorsAreDistinct(t *testing.T) {
	src := &memSource{}
	row := repoRow("api")
	src.set(row)
	for _, count := range []int{0, 1} {
		g := &historyGitFake{partial: true}
		if count == 1 {
			g.records = historyRows()[1:2]
		}
		got, partial, err := repos.NewHistoryReader(src, g).Commits(context.Background(), row.ID, historyFrom, historyFrom.Add(24*time.Hour), 200)
		if err != nil || !partial || len(got) != count {
			t.Fatal("capture cutoff lost", got, partial, err)
		}
	}
	failed := errors.New("pipe read failed")
	g := &historyGitFake{err: failed, partial: true, records: historyRows()}
	cs, partial, err := repos.NewHistoryReader(src, g).Commits(context.Background(), row.ID, historyFrom, historyFrom.Add(24*time.Hour), 200)
	if !errors.Is(err, failed) || !errors.Is(err, repos.ErrHistoryUnavailable) || cs != nil || partial {
		t.Fatal("error became data", cs, partial, err)
	}
}

func TestHistoryScopeBoundaryAndRecheck(t *testing.T) {
	src := &memSource{}
	row := repoRow("api")
	src.set(row)
	g := &historyGitFake{records: historyRows()}
	reader := repos.NewHistoryReader(src, g)
	if _, _, err := reader.Commits(context.Background(), "bad id", historyFrom, historyFrom.Add(time.Hour), 200); err == nil || g.calls != 0 {
		t.Fatal("bad ID reached git", err)
	}
	for _, change := range []func(*core.Repo){func(r *core.Repo) { r.Readiness = core.RepoReserved }, func(r *core.Repo) { r.ProjectID = "prj_foreign" }, func(r *core.Repo) { r.Path = "relative" }} {
		r := row
		change(&r)
		src.set(r)
		if _, _, err := reader.Commits(context.Background(), r.ID, historyFrom, historyFrom.Add(time.Hour), 200); !errors.Is(err, repos.ErrHistoryUnavailable) || g.calls != 0 {
			t.Fatal("scope reached git", err)
		}
	}
	src.set(row)
	g.after = func() { src.set() }
	if cs, _, err := reader.Commits(context.Background(), row.ID, historyFrom, historyFrom.Add(time.Hour), 200); !errors.Is(err, repos.ErrHistoryUnavailable) || cs != nil {
		t.Fatal("scope removal during read", cs, err)
	}
	src.set(row)
	g.after = func() { r := row; r.Path = "/work/changed"; src.set(r) }
	if cs, _, err := reader.Commits(context.Background(), row.ID, historyFrom, historyFrom.Add(time.Hour), 200); !errors.Is(err, repos.ErrHistoryUnavailable) || cs != nil {
		t.Fatal("path changed during read", cs, err)
	}
}

func TestHistoryScopeRealStoreProjectRemoval(t *testing.T) {
	e := newEnv(t)
	e.repoDir("api")
	e.track("acme-shop", "api")
	ctx := context.Background()
	ps, err := e.store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := e.store.ListRepos(ctx, ps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	g := &historyGitFake{records: historyRows()}
	reader := repos.NewHistoryReader(e.store, g)
	if _, _, err := reader.Commits(ctx, rs[0].ID, historyFrom, historyFrom.Add(time.Hour), 200); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UntrackProject(ctx, ps[0].ID, "user"); err != nil {
		t.Fatal(err)
	}
	before := g.calls
	if _, _, err := reader.Commits(ctx, rs[0].ID, historyFrom, historyFrom.Add(time.Hour), 200); !errors.Is(err, repos.ErrHistoryUnavailable) || g.calls != before {
		t.Fatal("project untracked but git called", err)
	}
}

func TestHistoryInvalidRangeNeverCallsCapability(t *testing.T) {
	src := &memSource{}
	src.set(repoRow("api"))
	g := &historyGitFake{}
	reader := repos.NewHistoryReader(src, g)
	for _, tc := range []struct {
		from, to time.Time
		max      int
	}{{time.Time{}, historyFrom, 1}, {historyFrom, time.Time{}, 1}, {historyFrom, historyFrom, 1}, {historyFrom.Add(time.Hour), historyFrom, 1}, {historyFrom, historyFrom.Add(31*24*time.Hour + time.Nanosecond), 1}, {historyFrom, historyFrom.Add(time.Hour), 0}, {historyFrom, historyFrom.Add(time.Hour), 201}} {
		_, _, err := reader.Commits(context.Background(), "rep_api", tc.from, tc.to, tc.max)
		if c, ok := apperr.ClassOf(err); !ok || c != apperr.Invalid || g.calls != 0 {
			t.Fatal("invalid range reached capability", err)
		}
	}
}

// Stateless live git can be shared by callers; fake uses one independent value
// per goroutine so its observation counter is not shared.
func TestHistoryConcurrentReads(t *testing.T) {
	fx := newGitFx(t)
	row := repoRow("api")
	row.Path = fx.repo("shop-api")
	fx.run(row.Path, "commit", "-q", "--allow-empty", "-m", "change", "--date="+historyFrom.Format(time.RFC3339))
	src := &memSource{}
	src.set(row)
	reader := repos.NewHistoryReader(src, fx.g)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cs, partial, err := reader.Commits(context.Background(), row.ID, historyFrom, historyFrom.Add(time.Hour), 200)
			if err != nil || partial || len(cs) != 1 {
				t.Errorf("concurrent history: %v %v %v", cs, partial, err)
			}
		}()
	}
	wg.Wait()
}

func TestHistoryLiveByteCutoffEmptyIsPartial(t *testing.T) {
	fx := newGitFx(t)
	row := repoRow("api")
	row.Path = fx.repo("shop-api")
	fx.write(row.Path, "message.txt", strings.Repeat("x", 60000))
	for i := 0; i < 20; i++ {
		fx.run(row.Path, "commit", "-q", "--allow-empty", "-F", "message.txt", "--date="+historyFrom.Add(-48*time.Hour).Format(time.RFC3339))
	}
	src := &memSource{}
	src.set(row)
	cs, partial, err := repos.NewHistoryReader(src, fx.g).Commits(context.Background(), row.ID, historyFrom, historyFrom.Add(time.Hour), 200)
	if err != nil || !partial || len(cs) != 0 {
		t.Fatal("byte cutoff became complete empty result", len(cs), partial, err)
	}
}

func TestHistoryUnbornAndUnreadable(t *testing.T) {
	fx := newGitFx(t)
	row := repoRow("api")
	row.Path = fx.repo("shop-api")
	src := &memSource{}
	src.set(row)
	reader := repos.NewHistoryReader(src, fx.g)
	cs, partial, err := reader.Commits(context.Background(), row.ID, historyFrom, historyFrom.Add(time.Hour), 200)
	if err != nil || partial || len(cs) != 0 {
		t.Fatal("unborn", cs, partial, err)
	}
	row.Path = t.TempDir()
	src.set(row)
	cs, partial, err = reader.Commits(context.Background(), row.ID, historyFrom, historyFrom.Add(time.Hour), 200)
	if !errors.Is(err, repos.ErrHistoryUnavailable) || cs != nil || partial {
		t.Fatal("unreadable became empty success", cs, partial, err)
	}
}
