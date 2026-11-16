package repos_test

// Contract suite of repos.NoiseSource (WS-09): the same clauses run over a
// scripted status reader (the fake) and over real git on temporary repos.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos/repostest"
)

// countingReader counts the status reads made through it.
type countingReader struct {
	inner repos.StatusReader
	reads atomic.Int64
}

func (c *countingReader) Read(ctx context.Context, p corerepos.CleanPath) (corerepos.Snapshot, corerepos.ProblemKind, error) {
	c.reads.Add(1)
	return c.inner.Read(ctx, p)
}

type noiseScenario struct {
	name  string
	files []string // untracked files, repo-relative, slash separated
	// wantCount, wantSample and wantPartial are what the reader contract promises.
	wantSample  int
	wantPartial bool
	// wantDirs are the candidate directories the analysis must offer.
	wantDirs []string
}

func numbered(dir string, n int, format string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = dir + "/" + fmt.Sprintf(format, i)
	}
	return out
}

func noiseScenarios() []noiseScenario {
	return []noiseScenario{
		{name: "below the threshold", files: []string{"a.txt", "b b.txt", "c/日本.txt"}, wantSample: 0, wantPartial: true},
		{name: "99 generated files", files: numbered("dist", 99, "f%03d"), wantSample: 0, wantPartial: true},
		{name: "exactly 100 with spaces", files: numbered("web app/node_modules", 100, "f %03d.txt"), wantSample: 100, wantDirs: []string{"web app/node_modules"}},
		{name: "unicode names and CRLF content", files: append(numbered("ünï 日本/dist", 120, "ファイル %03d.txt"), numbered("src", 10, "s%d.txt")...), wantSample: 130, wantDirs: []string{"ünï 日本/dist"}},
		{name: "mixed source and generated", files: append(numbered("src", 100, "s%03d.go"), numbered("build", 20, "b%02d")...), wantSample: 120},
		{name: "5100 files, capped sample", files: numbered("node_modules", 5100, "f%04d"), wantSample: 5000, wantPartial: true, wantDirs: []string{"node_modules"}},
	}
}

// fakeStatus is the git.Status real git produces for the files: sorted by path,
// capped at 5000 entries, with Truncated set when the cap cut the list.
func fakeStatus(files []string) git.Status {
	sorted := slices.Clone(files)
	sort.Strings(sorted)
	st := git.Status{Branch: "main", Untracked: len(sorted)}
	for _, f := range sorted {
		if len(st.Files) == 5000 {
			st.Truncated = true
			break
		}
		st.Files = append(st.Files, git.FileChange{Path: f, Index: "?", Worktree: "?"})
	}
	return st
}

func writeUnder(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// noiseWorld builds the reader and repo path for one scenario.
func noiseWorld(t *testing.T, live bool, sc noiseScenario) (*countingReader, corerepos.CleanPath) {
	t.Helper()
	if !live {
		snap, err := repos.SnapshotFrom(fakeStatus(sc.files), nil)
		if err != nil {
			t.Fatal(err)
		}
		fake := repostest.NewFakeReader()
		path := corerepos.CleanPath("/work/noise")
		fake.Script(path, repostest.FakeResult{Snap: snap})
		return &countingReader{inner: fake}, path
	}
	fx := newGitFx(t)
	dir := t.TempDir()
	fx.run(dir, "init", "-q", "-b", "main")
	for _, f := range sc.files {
		writeUnder(t, dir, f, "line one\r\nline two\r\n")
	}
	path, err := corerepos.NewCleanPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &countingReader{inner: repos.NewGitReader(fx.g)}, path
}

func TestNoiseSourceContractScenarios(t *testing.T) {
	for _, live := range []bool{false, true} {
		for _, sc := range noiseScenarios() {
			t.Run(fmt.Sprintf("live=%v/%s", live, sc.name), func(t *testing.T) {
				t.Parallel()
				reader, path := noiseWorld(t, live, sc)
				src := &memSource{}
				row := repoRow("noise")
				row.Path = path.String()
				src.set(row)
				svc := repos.NewStatusService(src, reader, time.Now, nil, repos.WithTickEvery(time.Hour))
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- svc.Start(ctx) }()
				defer func() {
					cancel()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				var source repos.NoiseSource = svc
				eventually(t, "noise facts", func() bool { _, err := source.NoiseFacts(ctx, row.ID); return err == nil })

				facts, err := source.NoiseFacts(ctx, row.ID)
				if err != nil {
					t.Fatal(err)
				}
				// Exact total, honest sample size, and the partial label.
				if facts.Repo != row.ID || facts.UntrackedCount != len(sc.files) || len(facts.Sample) != sc.wantSample || facts.IsPartial() != sc.wantPartial || facts.Truncated != sc.wantPartial {
					t.Fatalf("count=%d (want %d) sample=%d (want %d) partial=%v (want %v)", facts.UntrackedCount, len(sc.files), len(facts.Sample), sc.wantSample, facts.IsPartial(), sc.wantPartial)
				}
				// The sample is a prefix of the files in path order, exact bytes.
				sorted := slices.Clone(sc.files)
				sort.Strings(sorted)
				if !slices.Equal(facts.Sample, sorted[:len(facts.Sample)]) {
					t.Fatal("the sample is not the path-ordered prefix of the untracked files")
				}
				// The core analysis over those facts.
				n, cmds := corerepos.NoiseStep(corerepos.Noise{Repo: row.ID, State: corerepos.NoSuggestion{}}, corerepos.NoiseObserved{Facts: facts})
				if len(cmds) != 0 {
					t.Fatal("observation produced commands")
				}
				var dirs []string
				if s, ok := n.State.(corerepos.Suggested); ok {
					for _, c := range s.Candidates {
						dirs = append(dirs, c.Directory)
						if c.Incomplete != sc.wantPartial || c.Evidence.Total != len(sc.files) || c.Evidence.Sampled != sc.wantSample {
							t.Fatalf("candidate evidence %+v", c)
						}
					}
				}
				if !slices.Equal(dirs, sc.wantDirs) {
					t.Fatalf("candidates %v, want %v", dirs, sc.wantDirs)
				}

				// Repeated reads consume cached facts only: no further status read,
				// no refresh and no scan.
				reads := reader.reads.Load()
				for i := 0; i < 20; i++ {
					if _, err := source.NoiseFacts(ctx, row.ID); err != nil {
						t.Fatal(err)
					}
				}
				if reader.reads.Load() != reads {
					t.Fatalf("NoiseFacts triggered %d status reads", reader.reads.Load()-reads)
				}
				// Returned slices are the caller's: mutating one changes nothing else.
				if len(facts.Sample) > 0 {
					facts.Sample[0] = "caller mutation"
					again, err := source.NoiseFacts(ctx, row.ID)
					if err != nil || again.Sample[0] == "caller mutation" {
						t.Fatal("the returned sample aliases the cache", err)
					}
				}
				// Independent concurrent readers get identical results.
				var wg sync.WaitGroup
				results := make([]corerepos.NoiseFacts, 6)
				for i := range results {
					wg.Add(1)
					go func() {
						defer wg.Done()
						results[i], _ = source.NoiseFacts(ctx, row.ID)
					}()
				}
				wg.Wait()
				for i := 1; i < len(results); i++ {
					if !reflect.DeepEqual(results[0], results[i]) {
						t.Fatal("concurrent readers disagree")
					}
				}
				if reader.reads.Load() != reads {
					t.Fatal("concurrent NoiseFacts triggered a status read")
				}

				// Cancellation is unavailable, not data.
				stopped, stop := context.WithCancel(ctx)
				stop()
				if f, err := source.NoiseFacts(stopped, row.ID); err == nil || !errors.Is(err, repos.ErrNoiseUnavailable) || f.Sample != nil {
					t.Fatalf("cancelled read: %+v %v", f, err)
				}
				// Invalid and unknown IDs are unavailable.
				for _, id := range []core.RepoID{"", "bad id", "rep_unknown"} {
					if _, err := source.NoiseFacts(ctx, id); !errors.Is(err, repos.ErrNoiseUnavailable) {
						t.Fatalf("id %q: %v", id, err)
					}
				}
				// A changed path (the cached snapshot belongs to another folder) is
				// never served; restoring the row serves it again.
				moved := row
				moved.Path = path.String() + "-moved"
				src.set(moved)
				if _, err := source.NoiseFacts(ctx, row.ID); !errors.Is(err, repos.ErrNoiseUnavailable) {
					t.Fatal("a snapshot of another path was served", err)
				}
				src.set(row)
				if _, err := source.NoiseFacts(ctx, row.ID); err != nil {
					t.Fatalf("restored scope: %v", err)
				}
				// Reserved and removed scope win over the retained cache.
				reserved := row
				reserved.Readiness = core.RepoReserved
				src.set(reserved)
				if _, err := source.NoiseFacts(ctx, row.ID); !errors.Is(err, repos.ErrNoiseUnavailable) {
					t.Fatal("reserved repo exposed", err)
				}
				src.set()
				if _, err := source.NoiseFacts(ctx, row.ID); !errors.Is(err, repos.ErrNoiseUnavailable) {
					t.Fatal("untracked repo exposed", err)
				}
				if reader.reads.Load() != reads {
					t.Fatal("scope changes triggered a status read")
				}
			})
		}
	}
}

// Two tracked repos never see each other's cache, even when they share a name
// shape; and a stopped service is unavailable.
func TestNoiseFactsAreScopedPerRepoAndNeedARunningService(t *testing.T) {
	h := start(t, []string{"api", "web"}, nil)
	snap, err := repos.SnapshotFrom(fakeStatus(numbered("dist", 120, "f%03d")), nil)
	if err != nil {
		t.Fatal(err)
	}
	h.reader.Script(cpath("api"), repostest.FakeResult{Snap: snap})
	if err := h.svc.Refresh(context.Background(), []core.RepoID{"rep_api"}, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	eventually(t, "api noise", func() bool {
		f, err := h.svc.NoiseFacts(context.Background(), "rep_api")
		return err == nil && f.UntrackedCount == 120
	})
	f, err := h.svc.NoiseFacts(context.Background(), "rep_web")
	if err != nil || f.UntrackedCount != 0 || f.Repo != "rep_web" {
		t.Fatalf("another repo's facts leaked: %+v %v", f, err)
	}
	h.stop(t)
	if _, err := h.svc.NoiseFacts(context.Background(), "rep_api"); !errors.Is(err, repos.ErrNoiseUnavailable) {
		t.Fatalf("stopped service: %v", err)
	}
}

// A hostile name or a failed read makes noise unavailable while the ordinary
// status view of the same repo stays Fresh/usable.
func TestNoiseFailureLeavesOrdinaryStatusUsable(t *testing.T) {
	h := start(t, []string{"api"}, nil)
	hostile := fakeStatus(numbered("dist", 120, "f%03d"))
	hostile.Files[7].Path = "dist/evil\r\n"
	snap, err := repos.SnapshotFrom(hostile, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.reader.Script(cpath("api"), repostest.FakeResult{Snap: snap})
	if err := h.svc.Refresh(context.Background(), []core.RepoID{"rep_api"}, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	eventually(t, "fresh with 120 untracked", func() bool {
		s := h.snapshot(t)
		fresh, ok := s.Entries["rep_api"].View.(corerepos.Fresh)
		return ok && fresh.Snap.Untracked == 120
	})
	if _, err := h.svc.NoiseFacts(context.Background(), "rep_api"); !errors.Is(err, repos.ErrNoiseUnavailable) {
		t.Fatalf("hostile name served as evidence: %v", err)
	}
	s := h.snapshot(t)
	if v, ok := s.Entries["rep_api"].View.(corerepos.Fresh); !ok || v.Snap.Branch != "main" {
		t.Fatalf("ordinary view lost: %+v", s.Entries["rep_api"].View)
	}
	// An unknown repo is unavailable too, not an empty success.
	if _, err := h.svc.NoiseFacts(context.Background(), "rep_unknown"); !errors.Is(err, repos.ErrNoiseUnavailable) {
		t.Fatal(err)
	}
}

// The consumer interfaces expose reads only: no capability to write, pull,
// merge or refresh can reach through the noise or history ports.
func TestNoiseAndHistoryPortsAreReadOnly(t *testing.T) {
	methods := func(v any) []string {
		var out []string
		typ := reflect.TypeOf(v).Elem()
		for i := 0; i < typ.NumMethod(); i++ {
			out = append(out, typ.Method(i).Name)
		}
		return out
	}
	for _, tc := range []struct {
		port any
		want []string
	}{
		{(*repos.NoiseSource)(nil), []string{"NoiseFacts"}},
		{(*repos.HistoryReader)(nil), []string{"Commits"}},
		{(*repos.HistoryGit)(nil), []string{"LogRange"}},
	} {
		if got := methods(tc.port); !slices.Equal(got, tc.want) {
			t.Errorf("%T exposes %v, want only %v", tc.port, got, tc.want)
		}
	}
}

// Observing noise and history never changes the working tree, the index or any
// ref: the whole repo (including .git) is byte-identical afterwards, and an
// upstream that is ahead is not pulled.
func TestObservingNoiseAndHistoryNeverMutatesTheRepo(t *testing.T) {
	fx := newGitFx(t)
	dir := fx.repo("shop-api")
	// A fixed commit time, so the history window below does not use the clock.
	committedAt := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	fx.at(committedAt, committedAt).commit(dir, "a.txt", "a\r\n", "init")
	fx.run(dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	fx.run(dir, "config", "remote.origin.url", "https://example.invalid/acme/shop-api.git")
	fx.run(dir, "config", "branch.main.remote", "origin")
	fx.run(dir, "config", "branch.main.merge", "refs/heads/main")
	for i := 0; i < 120; i++ {
		writeUnder(t, dir, fmt.Sprintf("node_modules/f%03d", i), "x\r\n")
	}
	path, err := corerepos.NewCleanPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	src := &memSource{}
	row := repoRow("api")
	row.Path = path.String()
	src.set(row)
	svc := repos.NewStatusService(src, repos.NewGitReader(fx.g), time.Now, nil, repos.WithTickEvery(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Start(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	eventually(t, "noise facts", func() bool { _, err := svc.NoiseFacts(ctx, row.ID); return err == nil })
	before := treeHash(t, dir)
	history := repos.NewHistoryReader(src, fx.g)
	for i := 0; i < 5; i++ {
		if _, err := svc.NoiseFacts(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
		got, _, err := history.Commits(ctx, row.ID, committedAt.Add(-time.Hour), committedAt.Add(time.Hour), 50)
		if err != nil || len(got) != 1 {
			t.Fatalf("history read %d commits, err %v: the window must contain the commit", len(got), err)
		}
	}
	if after := treeHash(t, dir); after != before {
		t.Fatal("observing changed the repository on disk")
	}
}
