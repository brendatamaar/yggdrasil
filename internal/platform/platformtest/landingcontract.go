package platformtest

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// LandingFixture describes the folders a platform.Landing under test works
// on. The caller builds it: real temp dirs for platform.RealLanding
// (OSLandingFixture), an in-memory tree for FakeLanding. Optional funcs left
// nil skip their clauses.
type LandingFixture struct {
	// Root is an existing, canonical worktree root (the folder that holds
	// .removing).
	Root string
	// Mkdir creates a folder and its parents.
	Mkdir func(t *testing.T, dir string)
	// MakeTree creates dir with files "a.txt" and "sub/b.txt", a read-only
	// file "ro.txt" and an empty folder "empty".
	MakeTree func(t *testing.T, dir string)
	// MakeLongTree is MakeTree plus a path below dir longer than 260
	// characters. Optional.
	MakeLongTree func(t *testing.T, dir string)
	// Exists reports whether path exists (a link counts as existing itself).
	Exists func(path string) bool
	// Lock holds a handle open on a file inside dir, the way an editor does,
	// and returns the release func. Optional; nil where a handle cannot block
	// a rename (Unix).
	Lock func(t *testing.T, dir string) (release func())
	// Link makes link a junction or symlink to the existing folder target and
	// reports false if it cannot. Optional.
	Link func(t *testing.T, link, target string) bool
	// Short returns the 8.3 spelling of an existing path, or "" if it has
	// none. Optional.
	Short func(t *testing.T, path string) string
}

func blocked(c platform.LockClass) bool {
	return c == platform.LockSharing || c == platform.LockAccessDenied
}

// LandingPlatformContract is the contract suite of platform.Landing, run
// against the fake and the real implementation (docs/design/landing.md,
// docs/AGENTS.md 5.11). The refusal clauses check that nothing was deleted.
func LandingPlatformContract(t *testing.T, l platform.Landing, fx LandingFixture) {
	t.Helper()
	removing := filepath.Join(fx.Root, platform.StagingDir)
	n := 0
	fresh := func(parent string) string {
		n++
		return filepath.Join(parent, "wt"+string(rune('a'+n)))
	}
	staged := func(t *testing.T) string {
		t.Helper()
		fx.Mkdir(t, removing)
		return fresh(removing)
	}
	needTree := func(t *testing.T, dir string) {
		t.Helper()
		for _, f := range []string{"a.txt", filepath.Join("sub", "b.txt"), "ro.txt", "empty"} {
			if !fx.Exists(filepath.Join(dir, f)) {
				t.Fatalf("%s is missing from %s", f, dir)
			}
		}
	}

	t.Run("MoveDir moves the folder and leaves nothing", func(t *testing.T) {
		src := fresh(fx.Root)
		fx.MakeTree(t, src)
		dst := staged(t)
		if err := l.MoveDir(src, dst); err != nil {
			t.Fatalf("MoveDir: %v", err)
		}
		if fx.Exists(src) {
			t.Error("the source still exists")
		}
		needTree(t, dst)
	})

	t.Run("MoveDir fails when the target exists and changes nothing", func(t *testing.T) {
		src := fresh(fx.Root)
		fx.MakeTree(t, src)
		dst := staged(t)
		fx.Mkdir(t, dst)
		err := l.MoveDir(src, dst)
		var me *platform.MoveError
		if !errors.As(err, &me) {
			t.Fatalf("MoveDir onto an existing folder = %v, want *MoveError", err)
		}
		if me.Class == "" {
			t.Error("empty class")
		}
		needTree(t, src)
		if fx.Exists(filepath.Join(dst, "a.txt")) {
			t.Error("the target was replaced or merged")
		}
	})

	t.Run("MoveDir refuses the folder itself and its own subfolder", func(t *testing.T) {
		src := fresh(fx.Root)
		fx.MakeTree(t, src)
		for _, dst := range []string{src, filepath.Join(src, "sub", "x")} {
			var me *platform.MoveError
			if err := l.MoveDir(src, dst); !errors.As(err, &me) {
				t.Errorf("MoveDir(src, %q) = %v, want *MoveError", dst, err)
			}
		}
		needTree(t, src)
	})

	t.Run("MoveDir with a held handle fails as a lock and changes nothing", func(t *testing.T) {
		if fx.Lock == nil {
			t.Skip("a handle cannot block a rename here")
		}
		src := fresh(fx.Root)
		fx.MakeTree(t, src)
		dst := staged(t)
		release := fx.Lock(t, src)
		err := l.MoveDir(src, dst)
		var me *platform.MoveError
		if !errors.As(err, &me) {
			release()
			t.Fatalf("MoveDir with a held handle = %v, want *MoveError", err)
		}
		if !blocked(me.Class) || l.ClassifyLock(err) != me.Class {
			t.Errorf("class = %q (ClassifyLock %q), want sharing or access_denied", me.Class, l.ClassifyLock(err))
		}
		needTree(t, src)
		if fx.Exists(dst) {
			t.Error("the target exists after a failed move")
		}
		release()
		if err := l.MoveDir(src, dst); err != nil {
			t.Fatalf("MoveDir after the handle was closed: %v", err)
		}
		needTree(t, dst)
	})

	t.Run("ClassifyLock is never empty", func(t *testing.T) {
		for _, err := range []error{nil, errors.New("x"), errors.Join(errors.New("a"), errors.New("b"))} {
			if got := l.ClassifyLock(err); got != platform.LockOther {
				t.Errorf("ClassifyLock(%v) = %q, want other", err, got)
			}
		}
	})

	t.Run("RemoveStaged removes a tree with a read-only file", func(t *testing.T) {
		dir := staged(t)
		fx.MakeTree(t, dir)
		rep, err := l.RemoveStaged(fx.Root, dir)
		if err != nil || rep.Remaining != 0 {
			t.Fatalf("RemoveStaged = %+v, %v", rep, err)
		}
		if fx.Exists(dir) {
			t.Error("the folder still exists")
		}
		if !fx.Exists(removing) {
			t.Error(".removing itself was deleted")
		}
	})

	t.Run("RemoveStaged removes a tree with a path over 260 characters", func(t *testing.T) {
		if fx.MakeLongTree == nil {
			t.Skip("no long tree")
		}
		dir := staged(t)
		fx.MakeLongTree(t, dir)
		rep, err := l.RemoveStaged(fx.Root, dir)
		if err != nil || rep.Remaining != 0 {
			t.Fatalf("RemoveStaged = %+v, %v", rep, err)
		}
		if fx.Exists(dir) {
			t.Error("the folder still exists")
		}
	})

	t.Run("RemoveStaged is idempotent on a missing folder", func(t *testing.T) {
		dir := staged(t)
		for range 2 {
			if _, err := l.RemoveStaged(fx.Root, dir); err != nil {
				t.Fatalf("RemoveStaged(missing) = %v", err)
			}
		}
	})

	t.Run("RemoveStaged removes a link in the tree and not its target", func(t *testing.T) {
		if fx.Link == nil {
			t.Skip("no links here")
		}
		outside := fresh(fx.Root)
		fx.MakeTree(t, outside)
		dir := staged(t)
		fx.MakeTree(t, dir)
		if !fx.Link(t, filepath.Join(dir, "lnk"), outside) {
			t.Skip("cannot create a link here")
		}
		rep, err := l.RemoveStaged(fx.Root, dir)
		if err != nil || rep.Remaining != 0 {
			t.Fatalf("RemoveStaged = %+v, %v", rep, err)
		}
		if fx.Exists(dir) {
			t.Error("the folder still exists")
		}
		needTree(t, outside)
	})

	t.Run("RemoveStaged refuses paths it must not delete", func(t *testing.T) {
		victim := fresh(fx.Root) // a sibling of .removing
		fx.MakeTree(t, victim)
		dir := staged(t)
		fx.MakeTree(t, dir)
		sepStr := string(filepath.Separator)
		cases := map[string]string{
			"sibling of .removing":    victim,
			"the worktree root":       fx.Root,
			"the parent of the root":  filepath.Dir(fx.Root),
			".removing itself":        removing,
			"dotdot out":              dir + sepStr + ".." + sepStr + ".." + sepStr + filepath.Base(victim),
			"dotdot within":           removing + sepStr + "x" + sepStr + ".." + sepStr + filepath.Base(dir),
			"dotdot raw":              removing + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(victim),
			"empty":                   "",
			"relative":                filepath.Join(platform.StagingDir, filepath.Base(dir)),
			"inside a sibling":        filepath.Join(victim, "sub"),
			"name that only prefixes": removing + "x" + string(filepath.Separator) + "y",
		}
		for name, path := range cases {
			if _, err := l.RemoveStaged(fx.Root, path); err == nil {
				t.Errorf("%s: RemoveStaged(%q) = nil, want a refusal", name, path)
			}
		}
		needTree(t, victim)
		needTree(t, dir)
		if !fx.Exists(removing) || !fx.Exists(fx.Root) {
			t.Fatal("a folder that must stay was deleted")
		}

		if fx.Short != nil {
			if s := fx.Short(t, dir); s != "" && !strings.EqualFold(s, dir) {
				if _, err := l.RemoveStaged(fx.Root, s); err == nil {
					t.Errorf("the 8.3 spelling %q was accepted", s)
				}
				needTree(t, dir)
			}
		}
		if fx.Link != nil {
			link := filepath.Join(removing, "lnk")
			if fx.Link(t, link, victim) {
				if _, err := l.RemoveStaged(fx.Root, link); err == nil {
					t.Error("a link inside .removing that points out was accepted")
				}
				needTree(t, victim)
				if _, err := l.RemoveStaged(fx.Root, filepath.Join(link, "sub")); err == nil {
					t.Error("a path through a link that points out was accepted")
				}
				needTree(t, victim)
			}
		}
	})

	t.Run("RemoveStaged with a held handle reports what is left and resumes", func(t *testing.T) {
		if fx.Lock == nil {
			t.Skip("a handle cannot block a delete here")
		}
		dir := staged(t)
		fx.MakeTree(t, dir)
		release := fx.Lock(t, dir)
		rep, err := l.RemoveStaged(fx.Root, dir)
		if err == nil || rep.Remaining < 1 || !blocked(rep.Class) || rep.FirstErr == nil {
			release()
			t.Fatalf("RemoveStaged with a held handle = %+v, %v", rep, err)
		}
		if !fx.Exists(dir) {
			release()
			t.Fatal("the folder is gone although a file was held")
		}
		release()
		if rep, err := l.RemoveStaged(fx.Root, dir); err != nil || rep.Remaining != 0 {
			t.Fatalf("RemoveStaged after release = %+v, %v", rep, err)
		}
		if fx.Exists(dir) {
			t.Error("the folder still exists")
		}
	})

	t.Run("LockHolders is empty for a missing path and does not panic", func(t *testing.T) {
		if got := l.LockHolders(filepath.Join(fx.Root, "does-not-exist")); len(got) != 0 {
			t.Errorf("LockHolders(missing) = %v", got)
		}
		if got := l.LockHolders(""); len(got) != 0 {
			t.Errorf("LockHolders(\"\") = %v", got)
		}
	})
}
