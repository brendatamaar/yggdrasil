package repos_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestSnapshotNoiseKeepsOnlyUntracked(t *testing.T) {
	snap, err := repos.SnapshotFrom(git.Status{Untracked: 1, Files: []git.FileChange{{Path: "tracked", Worktree: "M"}, {Path: "dist/a", Worktree: "?"}}, Truncated: true}, nil)
	if err != nil || snap.UntrackedPaths != "dist/a" || !snap.UntrackedTruncated {
		t.Fatal(snap, err)
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
