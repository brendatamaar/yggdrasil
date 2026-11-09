package worktreeenvtest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

var contractNow = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

// StoreEnv is one PortStore + SettingsStore under test.
type StoreEnv struct {
	Ports    worktreeenv.PortStore
	Settings worktreeenv.SettingsStore
	// NewWorktree makes a worktree the store accepts rows for.
	NewWorktree func() core.WorktreeID
	// Repo is a repo the settings store knows; the store does not know "rep_unknown".
	Repo core.RepoID
	// Audit is the audit sink of the same store.
	Audit worktreeenv.AuditSink
	// Audits returns how many audit entries exist for the subject.
	Audits func(subject string) int
}

func entry(id, subject string) core.AuditEntry {
	return core.AuditEntry{ID: core.AuditID(id), At: contractNow, Actor: "system", Action: "worktreeenv.ports", Subject: subject}
}

// StoreContract runs the PortStore and SettingsStore contract.
func StoreContract(t *testing.T, mk func(t *testing.T) StoreEnv) {
	t.Helper()
	ctx := context.Background()
	blk := func(base int) core.PortBlock { return core.PortBlock{Base: base, Size: 20} }

	t.Run("a repeated audit ID is ErrConflict and writes nothing", func(t *testing.T) {
		e := mk(t)
		subject := "worktree:contract-dup"
		if err := e.Audit.Append(ctx, entry("aud_dup", subject)); err != nil {
			t.Fatal(err)
		}
		if err := e.Audit.Append(ctx, entry("aud_dup", subject)); !errors.Is(err, worktreeenv.ErrConflict) {
			t.Fatalf("second append = %v, want ErrConflict", err)
		}
		if n := e.Audits(subject); n != 1 {
			t.Fatalf("audit entries = %d, want 1", n)
		}
	})

	t.Run("ports round trip with audit", func(t *testing.T) {
		e := mk(t)
		w := e.NewWorktree()
		if got, err := e.Ports.LoadBlocks(ctx); err != nil || len(got) != 0 {
			t.Fatalf("fresh store: %v %v", got, err)
		}
		if err := e.Ports.SaveBlock(ctx, w, blk(20000), contractNow, entry("aud_1", "worktree:"+string(w))); err != nil {
			t.Fatal(err)
		}
		got, err := e.Ports.LoadBlocks(ctx)
		if err != nil || got[w] != blk(20000) || len(got) != 1 {
			t.Fatalf("got %v %v", got, err)
		}
		if n := e.Audits("worktree:" + string(w)); n != 1 {
			t.Fatalf("audit entries = %d", n)
		}
	})

	t.Run("one live block per worktree and per base, failed save writes nothing", func(t *testing.T) {
		e := mk(t)
		a, b := e.NewWorktree(), e.NewWorktree()
		if err := e.Ports.SaveBlock(ctx, a, blk(20000), contractNow, entry("aud_1", "worktree:"+string(a))); err != nil {
			t.Fatal(err)
		}
		err := e.Ports.SaveBlock(ctx, a, blk(20020), contractNow, entry("aud_2", "worktree:"+string(a)))
		if !errors.Is(err, worktreeenv.ErrConflict) {
			t.Fatalf("second block for one worktree: %v", err)
		}
		err = e.Ports.SaveBlock(ctx, b, blk(20000), contractNow, entry("aud_3", "worktree:"+string(b)))
		if !errors.Is(err, worktreeenv.ErrConflict) {
			t.Fatalf("same base for two worktrees: %v", err)
		}
		if n := e.Audits("worktree:" + string(b)); n != 0 {
			t.Fatalf("failed save left %d audit entries", n)
		}
		if got, _ := e.Ports.LoadBlocks(ctx); len(got) != 1 || got[a] != blk(20000) {
			t.Fatalf("blocks changed: %v", got)
		}
	})

	t.Run("release then reuse", func(t *testing.T) {
		e := mk(t)
		a, b := e.NewWorktree(), e.NewWorktree()
		_ = e.Ports.SaveBlock(ctx, a, blk(20000), contractNow, entry("aud_1", "worktree:"+string(a)))
		ok, err := e.Ports.ReleaseBlock(ctx, a, contractNow, entry("aud_2", "worktree:"+string(a)))
		if err != nil || !ok {
			t.Fatalf("release: %v %v", ok, err)
		}
		again, err := e.Ports.ReleaseBlock(ctx, a, contractNow, entry("aud_3", "worktree:"+string(a)))
		if err != nil || again {
			t.Fatalf("second release: %v %v", again, err)
		}
		if n := e.Audits("worktree:" + string(a)); n != 2 {
			t.Fatalf("audit entries = %d, want save + one release", n)
		}
		if err := e.Ports.SaveBlock(ctx, b, blk(20000), contractNow, entry("aud_4", "worktree:"+string(b))); err != nil {
			t.Fatalf("released base not reusable: %v", err)
		}
		if err := e.Ports.SaveBlock(ctx, a, blk(20020), contractNow, entry("aud_5", "worktree:"+string(a))); err != nil {
			t.Fatalf("released worktree cannot get a new block: %v", err)
		}
	})

	t.Run("settings defaults and round trip", func(t *testing.T) {
		e := mk(t)
		got, err := e.Settings.RepoEnv(ctx, e.Repo)
		if err != nil || got.AgentCopy || len(got.Profile) != 0 {
			t.Fatalf("defaults: %+v %v", got, err)
		}
		want := worktreeenv.RepoEnv{Profile: []env.RelPath{".env", "config/local.json"}, AgentCopy: true}
		if err := e.Settings.PutRepoEnv(ctx, e.Repo, want, contractNow, entry("aud_1", "repo:"+string(e.Repo))); err != nil {
			t.Fatal(err)
		}
		got, err = e.Settings.RepoEnv(ctx, e.Repo)
		if err != nil || !got.AgentCopy || !slices.Equal(got.Profile, want.Profile) {
			t.Fatalf("round trip: %+v %v", got, err)
		}
		if n := e.Audits("repo:" + string(e.Repo)); n != 1 {
			t.Fatalf("audit entries = %d", n)
		}
	})

	t.Run("settings of an unknown repo", func(t *testing.T) {
		e := mk(t)
		if _, err := e.Settings.RepoEnv(ctx, "rep_unknown"); !errors.Is(err, worktreeenv.ErrNotFound) {
			t.Fatalf("get: %v", err)
		}
		err := e.Settings.PutRepoEnv(ctx, "rep_unknown", worktreeenv.RepoEnv{}, contractNow, entry("aud_1", "repo:rep_unknown"))
		if !errors.Is(err, worktreeenv.ErrNotFound) {
			t.Fatalf("put: %v", err)
		}
		if n := e.Audits("repo:rep_unknown"); n != 0 {
			t.Fatalf("failed put left %d audit entries", n)
		}
	})

	t.Run("port range", func(t *testing.T) {
		e := mk(t)
		if r, err := e.Settings.PortRange(ctx); err != nil || r != env.DefaultRange {
			t.Fatalf("default: %v %v", r, err)
		}
		for i, r := range []env.Range{{Lo: 30000, Hi: 30999}, {Lo: 31000, Hi: 31999}} {
			if err := e.Settings.SetPortRange(ctx, r, entry("aud_r"+string(rune('a'+i)), "settings:range")); err != nil {
				t.Fatal(err)
			}
			if got, err := e.Settings.PortRange(ctx); err != nil || got != r {
				t.Fatalf("round trip: %v %v", got, err)
			}
		}
		if n := e.Audits("settings:range"); n != 2 {
			t.Fatalf("audit entries = %d", n)
		}
	})
}

// FSEnv is one FileSystem under test with two empty roots.
type FSEnv struct {
	FS       worktreeenv.FileSystem
	Src, Dst string
	// Write creates a file (and its folders) under root.
	Write func(root, rel string, data []byte)
	// Read returns a file under root.
	Read func(root, rel string) ([]byte, bool)
	// Link makes rel under root a link to something outside root, including a
	// folder with a file in it. It returns false when the system refuses to
	// make links (then the link cases are skipped).
	Link func(root, rel string) bool
	// Count returns how many entries exist under both roots (nothing may vanish).
	Count func() int
}

// FSContract runs the FileSystem contract.
func FSContract(t *testing.T, mk func(t *testing.T) FSEnv) {
	t.Helper()
	ctx := context.Background()
	observe := func(e FSEnv, rel env.RelPath) (env.Source, env.Dest) {
		src, dst, err := e.FS.Observe(ctx, e.Src, e.Dst, rel)
		if err != nil {
			t.Fatal(err)
		}
		return src, dst
	}

	t.Run("observe reports size, missing and folders", func(t *testing.T) {
		e := mk(t)
		e.Write(e.Src, "conf/app.env", []byte("A=1\r\nB=2\r\n"))
		if s, d := observe(e, "conf/app.env"); s != (env.SourceFile{Size: 10}) || d != env.DestAbsent {
			t.Fatalf("file: %#v %v", s, d)
		}
		if s, _ := observe(e, "nope.env"); s != (env.SourceMissing{}) {
			t.Fatalf("missing: %#v", s)
		}
		if s, _ := observe(e, "conf"); s != (env.SourceDirectory{}) {
			t.Fatalf("folder: %#v", s)
		}
		if s, _ := observe(e, "conf/app.env/deeper"); s != (env.SourceMissing{}) && s != (env.SourceDirectory{}) {
			t.Fatalf("below a file: %#v", s)
		}
		e.Write(e.Dst, "conf/app.env", []byte("x"))
		if _, d := observe(e, "conf/app.env"); d != env.DestExists {
			t.Fatalf("dest: %v", d)
		}
	})

	t.Run("observe refuses paths outside the rules", func(t *testing.T) {
		e := mk(t)
		e.Write(e.Src, ".git/config", []byte("secret"))
		e.Write(e.Src, "a.env", []byte("x"))
		for _, rel := range []env.RelPath{".git/config", "../a.env", "/a.env", "a/../a.env", ""} {
			if s, d := observe(e, rel); s != (env.SourceUnreadable{}) || d != env.DestExists {
				t.Errorf("%q: %#v %v", rel, s, d)
			}
		}
	})

	t.Run("links are refused at any level", func(t *testing.T) {
		e := mk(t)
		if !e.Link(e.Src, "linked.env") || !e.Link(e.Src, "linkdir") {
			t.Skip("this system does not allow links")
		}
		if s, _ := observe(e, "linked.env"); s != (env.SourceLink{}) {
			t.Fatalf("link as the file: %#v", s)
		}
		if s, _ := observe(e, "linkdir/secret.env"); s != (env.SourceLink{}) {
			t.Fatalf("link as a parent: %#v", s)
		}
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, "linkdir/secret.env", 1<<20); err == nil {
			t.Fatal("copied through a link")
		}
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, "linked.env", 1<<20); err == nil {
			t.Fatal("copied a link")
		}
		if _, ok := e.Read(e.Dst, "linkdir/secret.env"); ok {
			t.Fatal("a file appeared")
		}
	})

	t.Run("a link in the destination is never written through", func(t *testing.T) {
		e := mk(t)
		if !e.Link(e.Dst, "linkdir") {
			t.Skip("this system does not allow links")
		}
		e.Write(e.Src, "linkdir/new.env", []byte("x"))
		if _, d := observe(e, "linkdir/new.env"); d != env.DestExists {
			t.Fatalf("dest below a link: %v", d)
		}
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, "linkdir/new.env", 1<<20); err == nil {
			t.Fatal("wrote through a destination link")
		}
	})

	t.Run("copy keeps bytes and makes folders", func(t *testing.T) {
		e := mk(t)
		data := []byte("A=1\r\nB=2\r\n\x00\xff")
		e.Write(e.Src, "deep/er/app.env", data)
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, "deep/er/app.env", 1<<20); err != nil {
			t.Fatal(err)
		}
		if got, ok := e.Read(e.Dst, "deep/er/app.env"); !ok || !bytes.Equal(got, data) {
			t.Fatalf("copy = %q", got)
		}
		if got, ok := e.Read(e.Src, "deep/er/app.env"); !ok || !bytes.Equal(got, data) {
			t.Fatal("source changed")
		}
	})

	t.Run("copy never overwrites", func(t *testing.T) {
		e := mk(t)
		e.Write(e.Src, "a.env", []byte("new"))
		e.Write(e.Dst, "a.env", []byte("mine"))
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, "a.env", 1<<20); !errors.Is(err, worktreeenv.ErrDestExists) {
			t.Fatalf("got %v", err)
		}
		if got, _ := e.Read(e.Dst, "a.env"); string(got) != "mine" {
			t.Fatalf("overwritten: %q", got)
		}
	})

	t.Run("concurrent copies create the file once", func(t *testing.T) {
		e := mk(t)
		e.Write(e.Src, "race.env", []byte("payload"))
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = e.FS.CopyExclusive(ctx, e.Src, e.Dst, "race.env", 1<<20)
			}()
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, worktreeenv.ErrDestExists):
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if ok != 1 {
			t.Fatalf("%d copies succeeded, want 1", ok)
		}
		if got, _ := e.Read(e.Dst, "race.env"); string(got) != "payload" {
			t.Fatalf("content %q", got)
		}
	})

	t.Run("size cap and folders", func(t *testing.T) {
		e := mk(t)
		e.Write(e.Src, "big.bin", make([]byte, 100))
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, "big.bin", 99); !errors.Is(err, worktreeenv.ErrTooLarge) {
			t.Fatalf("got %v", err)
		}
		if _, ok := e.Read(e.Dst, "big.bin"); ok {
			t.Fatal("over-cap file was created")
		}
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, "big.bin", 100); err != nil {
			t.Fatalf("exactly at the cap: %v", err)
		}
		e.Write(e.Src, "dir/x", []byte("1"))
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, "dir", 100); err == nil {
			t.Fatal("copied a folder")
		}
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, "missing", 100); err == nil {
			t.Fatal("copied a missing file")
		}
		if err := e.FS.CopyExclusive(ctx, e.Src, e.Dst, ".git/config", 100); err == nil {
			t.Fatal("copied from .git")
		}
	})

	t.Run("nothing is removed", func(t *testing.T) {
		e := mk(t)
		e.Write(e.Src, "a.env", []byte("1"))
		e.Write(e.Dst, "b.env", []byte("2"))
		before := e.Count()
		_ = e.FS.CopyExclusive(ctx, e.Src, e.Dst, "a.env", 10)
		_ = e.FS.CopyExclusive(ctx, e.Src, e.Dst, "a.env", 10) // exists
		_ = e.FS.CopyExclusive(ctx, e.Src, e.Dst, "a.env", 0)  // too large
		if after := e.Count(); after < before {
			t.Fatalf("entries went from %d to %d", before, after)
		}
		if got, ok := e.Read(e.Dst, "b.env"); !ok || string(got) != "2" {
			t.Fatal("an existing file changed")
		}
	})
}

// ProberEnv is one PortProber under test.
type ProberEnv struct {
	Prober worktreeenv.PortProber
	// Listen makes a port busy and returns it; the port is freed at cleanup.
	Listen func(t *testing.T) int
	// Free returns a port nothing listens on.
	Free func(t *testing.T) int
}

// ProberContract runs the PortProber contract.
func ProberContract(t *testing.T, mk func(t *testing.T) ProberEnv) {
	t.Helper()
	ctx := context.Background()
	e := mk(t)
	busy := e.Listen(t)
	free := e.Free(t)
	if got, err := e.Prober.Busy(ctx, busy, busy); err != nil || !slices.Equal(got, []int{busy}) {
		t.Fatalf("listening port: %v %v", got, err)
	}
	if got, err := e.Prober.Busy(ctx, free, free); err != nil || len(got) != 0 {
		t.Fatalf("free port: %v %v", got, err)
	}
	lo, hi := min(busy, free), max(busy, free)
	if hi-lo < 2000 {
		got, err := e.Prober.Busy(ctx, lo, hi)
		if err != nil || !slices.Contains(got, busy) || slices.Contains(got, free) || !slices.IsSorted(got) {
			t.Fatalf("range: %v %v", got, err)
		}
	}
}
