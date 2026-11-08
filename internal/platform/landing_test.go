package platform_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

func TestRealLandingMeetsContract(t *testing.T) {
	// A root with spaces; on Windows the drive letter is part of the path.
	root := filepath.Join(canonDir(t), "work tree root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	platformtest.LandingPlatformContract(t, platform.RealLanding{}, platformtest.OSLandingFixture(root))
}

func TestFakeLandingMeetsContract(t *testing.T) {
	root := filepath.Join(canonDir(t), "fake root")
	f := platformtest.NewFakeLanding()
	fx := f.Fixture(root)
	fx.Mkdir(t, root)
	platformtest.LandingPlatformContract(t, f, fx)
}

func TestFakeLandingScriptedLock(t *testing.T) {
	root := filepath.Join(canonDir(t), "r")
	f := platformtest.NewFakeLanding()
	fx := f.Fixture(root)
	src := filepath.Join(root, "wt")
	fx.MakeTree(t, src)
	fx.Mkdir(t, filepath.Join(root, platform.StagingDir))
	f.SetHolders(src, "Code.exe")
	release := f.SetLock(src, platform.LockAccessDenied)
	err := f.MoveDir(src, filepath.Join(root, platform.StagingDir, "wt-1"))
	var me *platform.MoveError
	if !errors.As(err, &me) || me.Class != platform.LockAccessDenied || f.ClassifyLock(err) != platform.LockAccessDenied {
		t.Fatalf("scripted lock = %v", err)
	}
	if got := f.LockHolders(src); len(got) != 1 || got[0] != "Code.exe" {
		t.Errorf("LockHolders = %v", got)
	}
	release()
	if err := f.MoveDir(src, filepath.Join(root, platform.StagingDir, "wt-1")); err != nil {
		t.Errorf("after release: %v", err)
	}
}

func TestMoveDirRefusesBadPaths(t *testing.T) {
	root := canonDir(t)
	src := filepath.Join(root, "src")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := []string{"", "relative" + string(filepath.Separator) + "x", filepath.Join(root, "a\x00b")}
	if runtime.GOOS == "windows" {
		bad = append(bad,
			`\\server\share\x`, `\\wsl$\Ubuntu\home\x`, `\\.\C:\x`, `\\?\GLOBALROOT\x`,
			filepath.Join(root, "a:b"), filepath.Join(root, "NUL"), filepath.Join(root, "dot."), `C:\`)
	}
	for _, to := range bad {
		var me *platform.MoveError
		err := platform.MoveDir(src, to)
		if !errors.As(err, &me) || me.Class == "" {
			t.Errorf("MoveDir(src, %q) = %v, want *MoveError", to, err)
		}
		err = platform.MoveDir(to, filepath.Join(root, "dst"))
		if !errors.As(err, &me) || me.Class == "" {
			t.Errorf("MoveDir(%q, dst) = %v, want *MoveError", to, err)
		}
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the source changed: %v", err)
	}
}

func TestMoveDirMissingSourceIsLockOther(t *testing.T) {
	root := canonDir(t)
	err := platform.MoveDir(filepath.Join(root, "nope"), filepath.Join(root, "dst"))
	var me *platform.MoveError
	if !errors.As(err, &me) || me.Class != platform.LockOther {
		t.Fatalf("MoveDir(missing) = %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error should wrap not-exist: %v", err)
	}
}

func TestMoveDirDoesNotLeakPaths(t *testing.T) {
	root := canonDir(t)
	secret := filepath.Join(root, "secret-folder-name")
	err := platform.MoveDir(secret, filepath.Join(root, "dst"))
	if err == nil || containsStr(err.Error(), "secret-folder-name") {
		t.Fatalf("error text holds the path or is nil: %v", err)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestClassifyLockWrapping(t *testing.T) {
	en := lockErrno(platform.LockSharing)
	for name, err := range map[string]error{
		"bare":     en,
		"link":     &os.LinkError{Op: "rename", Old: "a", New: "b", Err: en},
		"path":     &os.PathError{Op: "remove", Path: "a", Err: en},
		"join":     errors.Join(errors.New("x"), &os.PathError{Op: "remove", Path: "a", Err: en}),
		"wrapped":  &platform.MoveError{Class: platform.LockOther, Err: &os.LinkError{Err: en}},
		"fmt wrap": errors.Join(errors.New("y"), en),
	} {
		if got := platform.ClassifyLock(err); got != platform.LockSharing {
			t.Errorf("%s: ClassifyLock = %q, want sharing", name, got)
		}
	}
	if got := platform.ClassifyLock(nil); got != platform.LockOther {
		t.Errorf("nil = %q", got)
	}
	if got := platform.ClassifyLock(syscall.Errno(0x7fff)); got != platform.LockOther {
		t.Errorf("unknown = %q", got)
	}
	if got := platform.ClassifyLock(errors.New("plain")); got != platform.LockOther {
		t.Errorf("plain = %q", got)
	}
}
