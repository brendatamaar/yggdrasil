package repos_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// These tests are the always-on counterpart of TestPerf_StatusRefresh50Repos:
// the same counts, with a handful of real repos, so removing the object-store
// read or the commit cache fails `task test`, not only `task perf`.

// statusOnly hides HeadCommit, so the reader has to run git log.
type statusOnly struct{ g *git.Git }

func (s statusOnly) Status(ctx context.Context, dir string) (git.Status, error) {
	return s.g.Status(ctx, dir)
}

func (s statusOnly) Log(ctx context.Context, dir string, n int) ([]git.Commit, error) {
	return s.g.Log(ctx, dir, n)
}

func (c *countingGit) logCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.log
}

func cleanPath(t *testing.T, dir string) corerepos.CleanPath {
	t.Helper()
	p, err := corerepos.NewCleanPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// realRepos makes n repos (copies of one template) under a path with a space.
func realRepos(t *testing.T, f *gitFx, n int) []corerepos.CleanPath {
	t.Helper()
	tpl := perfTemplate(f, "tpl", false)
	root := filepath.Join(t.TempDir(), "acme shop")
	var out []corerepos.CleanPath
	for i := range n {
		dir := filepath.Join(root, fmt.Sprintf("shop-%02d", i))
		copyTree(t, tpl, dir)
		out = append(out, cleanPath(t, dir))
	}
	return out
}

func TestGitReader_RealRepos_DirectReadAndCacheCounts(t *testing.T) {
	f := newGitFx(t)
	const n = 6
	paths := realRepos(t, f, n)
	cg := &countingGit{g: f.g}
	r := repos.NewGitReader(cg)
	ctx := context.Background()

	readAll := func() {
		t.Helper()
		for _, p := range paths {
			snap, kind, err := r.Read(ctx, p)
			if err != nil || kind != "" {
				t.Fatalf("read %s: %v %q", p, err, kind)
			}
			if snap.LastCommit == nil || (snap.LastCommit.Subject != "init" && snap.LastCommit.Subject != "second") {
				t.Fatalf("last commit of %s = %+v", p, snap.LastCommit)
			}
		}
	}

	readAll() // startup
	if got := cg.direct.Load(); got != n {
		t.Errorf("startup: %d direct reads for %d repos, want one per repo", got, n)
	}
	if got := cg.logCalls(); got != 0 {
		t.Errorf("startup: git log ran %d times, want 0 (every commit is readable directly)", got)
	}

	readAll() // user refresh, nothing changed
	if got := cg.direct.Load(); got != n {
		t.Errorf("refresh: direct reads rose to %d, want %d (the oid cache should answer)", got, n)
	}
	if got := cg.logCalls(); got != 0 {
		t.Errorf("refresh: git log ran %d times, want 0", got)
	}
	cg.mu.Lock()
	statusCalls := cg.status
	cg.mu.Unlock()
	if statusCalls != 2*n {
		t.Errorf("git status ran %d times for %d reads, want one per read", statusCalls, 2*n)
	}

	// A new commit is a new oid: it must be read again, not served from the cache.
	f.commit(paths[0].String(), "new.txt", "n\n", "second")
	snap, _, err := r.Read(ctx, paths[0])
	if err != nil || snap.LastCommit == nil || snap.LastCommit.Subject != "second" {
		t.Fatalf("after a new commit: %+v %v", snap.LastCommit, err)
	}
	if got := cg.direct.Load(); got != n+1 {
		t.Errorf("direct reads = %d after one new commit, want %d", got, n+1)
	}

	// Retain forgets untracked folders: reading one again costs a direct read.
	r.Retain(paths[:1])
	readAll()
	if got := cg.direct.Load(); got != n+1+(n-1) {
		t.Errorf("direct reads = %d after Retain dropped %d repos, want %d", got, n-1, n+1+(n-1))
	}
}

// Both paths (direct read and git log) must give the same snapshot, field by field.
func TestGitReader_RealRepos_DirectEqualsLog(t *testing.T) {
	f := newGitFx(t)
	paths := realRepos(t, f, 3)
	f.run(paths[1].String(), "-c", "i18n.commitEncoding=ISO-8859-1", "commit", "-q", "--allow-empty", "-m", "latin subject")
	f.run(paths[2].String(), "commit", "-q", "--allow-empty", "--date=2020-01-02T03:04:05-0330", "-m", "odd zone")
	direct := repos.NewGitReader(&countingGit{g: f.g})
	viaLog := repos.NewGitReader(statusOnly{g: f.g})
	for _, p := range paths {
		a, _, err1 := direct.Read(context.Background(), p)
		b, _, err2 := viaLog.Read(context.Background(), p)
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		la, lb := a.LastCommit, b.LastCommit
		if la == nil || lb == nil {
			t.Fatalf("no commit: %+v %+v", la, lb)
		}
		if la.OID != lb.OID || la.Subject != lb.Subject || la.Author != lb.Author || !la.At.Equal(lb.At) ||
			la.At.Format("-07:00") != lb.At.Format("-07:00") {
			t.Errorf("%s: direct %+v, log %+v", p, la, lb)
		}
	}
}

// Hammer: many goroutines read while others flip Retain. Run under `task
// test:race` (needs gcc). Every answer must belong to the repo that was read.
func TestGitReader_ConcurrentReadAndRetain(t *testing.T) {
	var directCalls atomic.Int64
	sg := &scriptedGit{oid: "aaa"}
	sg.head = func(dir, oid string) (git.Commit, bool) {
		directCalls.Add(1)
		return git.Commit{Hash: oid, Subject: "direct " + oid + " " + filepath.Base(dir), Author: "Ada", Parents: []string{"p1", "p2"}}, true
	}
	r := repos.NewGitReader(sg)
	paths := make([]corerepos.CleanPath, 4)
	for i := range paths {
		paths[i] = cachePath(t)
	}

	const readers, perReader = 12, 300
	ctx, cancel := context.WithCancel(context.Background())
	var retainers sync.WaitGroup
	for i := range 2 {
		retainers.Add(1)
		go func() {
			defer retainers.Done()
			for k := 0; ctx.Err() == nil; k++ {
				if (k+i)%2 == 0 {
					r.Retain(paths[:2])
				} else {
					r.Retain(nil)
				}
			}
		}()
	}
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for g := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range perReader {
				p := paths[(g+k)%len(paths)]
				snap, _, err := r.Read(context.Background(), p)
				if err != nil {
					errs <- err
					return
				}
				want := "direct aaa " + filepath.Base(p.String())
				if snap.LastCommit == nil || snap.LastCommit.Subject != want {
					errs <- fmt.Errorf("read of %s gave %+v", p, snap.LastCommit)
					return
				}
			}
		}()
	}
	wg.Wait()
	cancel()
	retainers.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := sg.logCalls(); n != 0 {
		t.Errorf("git log ran %d times although the direct read always answers", n)
	}
	if directCalls.Load() < int64(len(paths)) {
		t.Errorf("only %d direct reads for %d folders", directCalls.Load(), len(paths))
	}
}

// Hammer with HEAD moving: the answer must always match the oid git reported
// at the end of the read, and a settled state must be served correctly.
func TestGitReader_ConcurrentReadWhileHeadMoves(t *testing.T) {
	sg := &scriptedGit{oid: "aaa"}
	r := repos.NewGitReader(sg)
	p := cachePath(t)
	ctx, cancel := context.WithCancel(context.Background())
	var mover sync.WaitGroup
	mover.Add(1)
	go func() {
		defer mover.Done()
		for k := 0; ctx.Err() == nil; k++ {
			sg.setOID([]string{"aaa", "bbb", "ccc"}[k%3])
		}
	}()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				if _, _, err := r.Read(context.Background(), p); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	cancel()
	mover.Wait()
	sg.setOID("zzz")
	if got := readLast(t, r, p); got != "subject zzz" {
		t.Errorf("after HEAD settled on zzz the reader answered %q", got)
	}
}
