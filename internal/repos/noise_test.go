package repos_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos/repostest"
)

func TestNoiseSnapshotBoundary(t *testing.T) {
	for _, raw := range []string{"dist/a", "日本語/build/a", "a[?*] #!/dist/a"} {
		snap := corerepos.Snapshot{Untracked: 1, UntrackedPaths: raw}
		f, err := repos.ParseNoiseSnapshot("rep_1", snap)
		if err != nil || len(f.Sample) != 1 || f.Sample[0] != raw || f.Truncated {
			t.Fatal(f, err)
		}
		again, err := repos.ParseNoiseSnapshot(f.Repo, corerepos.Snapshot{Untracked: f.UntrackedCount, UntrackedPaths: strings.Join(f.Sample, "\x00"), UntrackedTruncated: f.Truncated})
		if err != nil || !reflect.DeepEqual(f, again) {
			t.Fatal("roundtrip", again, err)
		}
	}
	for _, raw := range []string{"../dist/a", "/dist/a", `C:/dist/a`, "dist/a\r", "dist/a\n", "dist/a\x00dist/a"} {
		_, err := repos.ParseNoiseSnapshot("rep_1", corerepos.Snapshot{Untracked: 2, UntrackedPaths: raw})
		if !errors.Is(err, repos.ErrNoiseUnavailable) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	f, err := repos.ParseNoiseSnapshot("rep_1", corerepos.Snapshot{Untracked: 100, UntrackedPaths: "dist/a"})
	if err != nil || !f.Truncated {
		t.Fatal("missing evidence not labelled", f, err)
	}
	for _, snap := range []corerepos.Snapshot{{Untracked: -1}, {Untracked: 1, UntrackedPaths: "a\x00b"}, {Untracked: 6000, UntrackedPaths: strings.Repeat("a\x00", 5001)}} {
		if _, err := repos.ParseNoiseSnapshot("rep_1", snap); !errors.Is(err, repos.ErrNoiseUnavailable) {
			t.Fatal("invalid facts accepted", snap.Untracked, err)
		}
	}
}

// The same consumer contract runs over the scripted status reader and the
// real safe git reader on a temporary 5,100-file repo with CRLF content.
func TestNoiseSourceContract(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprint("live=", live), func(t *testing.T) {
			path := cpath("noise")
			var reader repos.StatusReader
			if live {
				fx := newGitFx(t)
				dir := t.TempDir()
				fx.run(dir, "init", "-q", "-b", "main")
				if err := os.Mkdir(filepath.Join(dir, "node_modules"), 0o700); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 5100; i++ {
					fx.write(dir, filepath.Join("node_modules", fmt.Sprintf("f%04d", i)), "line\r\n")
				}
				var err error
				path, err = corerepos.NewCleanPath(dir)
				if err != nil {
					t.Fatal(err)
				}
				reader = repos.NewGitReader(fx.g)
			} else {
				f := repostest.NewFakeReader()
				var paths []string
				for i := 0; i < 5000; i++ {
					paths = append(paths, fmt.Sprintf("node_modules/f%04d", i))
				}
				f.Script(path, repostest.FakeResult{Snap: corerepos.Snapshot{Untracked: 5100, UntrackedPaths: strings.Join(paths, "\x00"), UntrackedTruncated: true}})
				reader = f
			}
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
			eventually(t, "noise available", func() bool { _, err := svc.NoiseFacts(ctx, row.ID); return err == nil })
			var source repos.NoiseSource = svc
			facts, err := source.NoiseFacts(ctx, row.ID)
			if err != nil || facts.UntrackedCount != 5100 || len(facts.Sample) != 5000 || !facts.Truncated {
				t.Fatalf("count=%d sample=%d truncated=%v error=%v", facts.UntrackedCount, len(facts.Sample), facts.Truncated, err)
			}
			n, cs := corerepos.NoiseStep(corerepos.Noise{Repo: row.ID, State: corerepos.NoSuggestion{}}, corerepos.NoiseObserved{Facts: facts})
			if len(cs) != 0 || !n.State.(corerepos.Suggested).Candidates[0].Incomplete {
				t.Fatal(n, cs)
			}
			facts.Sample[0] = "caller mutation"
			again, err := source.NoiseFacts(ctx, row.ID)
			if err != nil || again.Sample[0] == "caller mutation" {
				t.Fatal("sample alias", err)
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for j := 0; j < 10; j++ {
						_, _ = source.NoiseFacts(ctx, row.ID)
					}
				}()
			}
			wg.Wait()
			src.set() // cache still holds facts, but scope must win before a reload
			if _, err := source.NoiseFacts(ctx, row.ID); !errors.Is(err, repos.ErrNoiseUnavailable) {
				t.Fatal("untracked cache visible", err)
			}
			row.Readiness = core.RepoReserved
			src.set(row)
			if _, err := source.NoiseFacts(ctx, row.ID); !errors.Is(err, repos.ErrNoiseUnavailable) {
				t.Fatal("reserved cache visible", err)
			}
			cancelled, stop := context.WithCancel(ctx)
			stop()
			if _, err := source.NoiseFacts(cancelled, row.ID); err == nil {
				t.Fatal("cancelled read accepted")
			}
		})
	}
}

func TestNoiseCollectorRejectsProblemLastSnapshot(t *testing.T) {
	h := start(t, []string{"api"}, nil)
	eventually(t, "fresh", h.allView(t, 1, isFresh))
	h.reader.Script(cpath("api"), repostest.FakeResult{Kind: corerepos.ProblemMissing, Err: errors.New("missing")})
	if err := h.svc.Refresh(context.Background(), []core.RepoID{"rep_api"}, corerepos.ReasonUser); err != nil {
		t.Fatal(err)
	}
	eventually(t, "problem", h.allView(t, 1, func(v corerepos.View) bool { _, ok := v.(corerepos.Problem); return ok }))
	if _, err := h.svc.NoiseFacts(context.Background(), "rep_api"); !errors.Is(err, repos.ErrNoiseUnavailable) {
		t.Fatal("stale facts exposed", err)
	}
}

// untrackedStatus builds a git status with n untracked files under dist plus one
// tracked change; the tracked path must never reach the noise sample.
func untrackedStatus(n int, pad int, truncated bool) git.Status {
	st := git.Status{Untracked: n, Truncated: truncated, Files: []git.FileChange{{Path: "tracked", Worktree: "M"}}}
	for i := 0; i < n; i++ {
		st.Files = append(st.Files, git.FileChange{Path: fmt.Sprintf("dist/%0*d", pad, i), Worktree: "?"})
	}
	return st
}

func TestSnapshotNoiseKeepsOnlyUntracked(t *testing.T) {
	st := untrackedStatus(corerepos.NoiseMinUntracked, 1, true)
	snap, err := repos.SnapshotFrom(st, nil)
	paths := strings.Split(snap.UntrackedPaths, "\x00")
	if err != nil || len(paths) != corerepos.NoiseMinUntracked || paths[0] != "dist/0" || slices.Contains(paths, "tracked") || !snap.UntrackedTruncated {
		t.Fatal(len(paths), snap.UntrackedTruncated, err)
	}
}

// Below NoiseMinUntracked the sample is dropped and only the count stays, so
// 5000 long paths never sit in (or get copied with) every repo's snapshot. At
// and above the threshold the sample is kept in full.
func TestSnapshotDropsUntrackedSampleBelowNoiseThreshold(t *testing.T) {
	const pathLen = 4000
	below := untrackedStatus(corerepos.NoiseMinUntracked-1, pathLen, false)
	// A status that lists far more untracked paths than it counts still keeps
	// nothing: the count decides, so memory stays bounded.
	for i := 0; i < 5000; i++ {
		below.Files = append(below.Files, git.FileChange{Path: fmt.Sprintf("dist/%0*d", pathLen, 10000+i), Worktree: "?"})
	}
	snap, err := repos.SnapshotFrom(below, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.UntrackedPaths != "" || snap.Untracked != corerepos.NoiseMinUntracked-1 {
		t.Fatalf("below the threshold: %d sample bytes, count %d", len(snap.UntrackedPaths), snap.Untracked)
	}
	snap, err = repos.SnapshotFrom(untrackedStatus(5000, 100, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(snap.UntrackedPaths, "\x00") + 1; got != 5000 || len(snap.UntrackedPaths) > 5000*(4096+1) {
		t.Fatalf("above the threshold: %d paths, %d bytes", got, len(snap.UntrackedPaths))
	}
	if snap.UntrackedTruncated {
		t.Fatal("a complete sample was marked partial")
	}
}

// The facts carry an explicit partial-sample marker (Truncated, IsPartial): it
// is set whenever the kept sample is shorter than the untracked count, whether
// the reader truncated or the sample was dropped, so a UI can say "based on a
// partial sample". The snapshot itself only records what the reader reported.
func TestSnapshotMarksPartialSample(t *testing.T) {
	short := untrackedStatus(corerepos.NoiseMinUntracked, 1, false)
	short.Untracked += 7
	for _, tc := range []struct {
		name string
		st   git.Status
		want bool
	}{
		{"complete", untrackedStatus(corerepos.NoiseMinUntracked, 1, false), false},
		{"reader truncated", untrackedStatus(corerepos.NoiseMinUntracked, 1, true), true},
		{"fewer paths than counted", short, true},
		{"dropped below the threshold", untrackedStatus(3, 1, false), true},
		{"nothing untracked", untrackedStatus(0, 1, false), false},
	} {
		snap, err := repos.SnapshotFrom(tc.st, nil)
		if err != nil {
			t.Fatal(err)
		}
		facts, err := repos.ParseNoiseSnapshot("rep_1", snap)
		if err != nil || facts.IsPartial() != tc.want {
			t.Errorf("%s: facts partial = %v, want %v (%v)", tc.name, facts.IsPartial(), tc.want, err)
		}
	}
}

// Dropping the sample below the threshold changes no noise decision: for random
// statuses, the step over the stored snapshot equals the step over facts built
// from the full, undropped sample.
func TestSnapshotSampleDropKeepsNoiseDecisions(t *testing.T) {
	dirsPool := []string{"node_modules", "dist", "src", "build/x", "web/node_modules"}
	suggested := 0
	for seed := int64(0); seed < 200; seed++ {
		r := rand.New(rand.NewSource(seed))
		n := r.Intn(220)
		st := git.Status{Untracked: n, Truncated: r.Intn(4) == 0}
		var full []string
		pick := dirsPool[r.Intn(len(dirsPool))]
		for i := 0; i < n; i++ {
			dir := pick
			if r.Intn(4) == 0 {
				dir = dirsPool[r.Intn(len(dirsPool))]
			}
			p := fmt.Sprintf("%s/f%d", dir, i)
			full = append(full, p)
			st.Files = append(st.Files, git.FileChange{Path: p, Worktree: "?"})
		}
		snap, err := repos.SnapshotFrom(st, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := repos.ParseNoiseSnapshot("rep_1", snap)
		if err != nil {
			t.Fatal(err)
		}
		undropped := corerepos.NoiseFacts{Repo: "rep_1", UntrackedCount: n, Sample: full, Truncated: st.Truncated || len(full) < n}
		start := corerepos.Noise{Repo: "rep_1", State: corerepos.NoSuggestion{}}
		a, ca := corerepos.NoiseStep(start, corerepos.NoiseObserved{Facts: got})
		b, cb := corerepos.NoiseStep(start, corerepos.NoiseObserved{Facts: undropped})
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ca, cb) {
			t.Fatalf("seed %d (n=%d): dropped %+v, undropped %+v", seed, n, a, b)
		}
		if _, ok := a.State.(corerepos.Suggested); ok {
			suggested++
		}
	}
	if suggested == 0 {
		t.Fatal("no generated status ever suggested noise: the comparison is vacuous")
	}
}

func FuzzParseNoiseSnapshot(f *testing.F) {
	f.Add("dist/a", 1, false)
	f.Add("dist/a\x00dist/b", 2, true)
	f.Add("../bad", 1, false)
	f.Fuzz(func(t *testing.T, raw string, count int, truncated bool) {
		facts, err := repos.ParseNoiseSnapshot("rep_1", corerepos.Snapshot{Untracked: count, UntrackedPaths: raw, UntrackedTruncated: truncated})
		if err == nil {
			if facts.UntrackedCount < 0 || len(facts.Sample) > 5000 || (!facts.Truncated && len(facts.Sample) != facts.UntrackedCount) {
				t.Fatal("invalid accepted facts")
			}
		}
	})
}

func TestNoisePatternGitContract(t *testing.T) {
	fx := newGitFx(t)
	dir := t.TempDir()
	fx.run(dir, "init", "-q", "-b", "main")
	var paths []string
	for i := 0; i < 100; i++ {
		paths = append(paths, fmt.Sprintf("web/[app] #!/build/f%d", i))
	}
	if err := os.MkdirAll(filepath.Join(dir, "web", "[app] #!", "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		fx.write(dir, p, "generated\r\n")
	}
	clean, err := corerepos.NewCleanPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err := repos.NewGitReader(fx.g).Read(context.Background(), clean)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := repos.ParseNoiseSnapshot("rep_1", snap)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := corerepos.NoiseStep(corerepos.Noise{Repo: "rep_1", State: corerepos.NoSuggestion{}}, corerepos.NoiseObserved{Facts: facts})
	pattern := n.State.(corerepos.Suggested).Candidates[0].Pattern
	fx.write(dir, ".gitignore", "# preserve CRLF\r\n"+pattern+"\r\n")
	fx.run(dir, "check-ignore", "--no-index", "-q", "--", paths[0])
	for _, p := range []string{"other/web/[app] #!/build/f0", "web/a #!/build/f0", "web/[app] #!/builder/f0", "src/file"} {
		if err := fx.runErr(dir, "check-ignore", "--no-index", "-q", "--", p); err == nil {
			t.Errorf("pattern %q also ignored %q", pattern, p)
		}
	}
}
