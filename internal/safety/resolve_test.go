package safety_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// touchTool makes "tool" and "tool.cmd" in dir, so the test holds on Windows
// (PATHEXT) and elsewhere (exec bit) without a GOOS check.
func touchTool(t *testing.T, dir string) {
	t.Helper()
	for _, n := range []string{"tool", "tool.cmd"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o755); err != nil { //nolint:gosec // test executable stand-in
			t.Fatal(err)
		}
	}
}

func TestResolveProgramUsesPathNotCwd(t *testing.T) {
	pathDir, cwd := t.TempDir(), t.TempDir()
	touchTool(t, pathDir)
	touchTool(t, cwd) // the decoy must not win
	env := []string{"PATH=" + pathDir, "PATHEXT=.CMD;.EXE"}
	ref, err := safety.ResolveProgram("tool", cwd, env)
	if err != nil || !ref.OK {
		t.Fatalf("ref = %+v, %v", ref, err)
	}
	want, _ := platform.ResolveRoot(pathDir)
	if filepath.Dir(ref.Canon) != want {
		t.Fatalf("resolved to %s, want a file in %s", ref.Canon, want)
	}
	if ref.Raw != "tool" || !filepath.IsAbs(ref.Canon) {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestResolveProgramNotOnPathEvenIfInCwd(t *testing.T) {
	pathDir, cwd := t.TempDir(), t.TempDir()
	touchTool(t, cwd)
	ref, err := safety.ResolveProgram("tool", cwd, []string{"PATH=" + pathDir})
	if err == nil || ref.OK {
		t.Fatalf("resolved a program found only in the cwd: %+v", ref)
	}
}

func TestResolveProgramRelativePathEntryIgnored(t *testing.T) {
	cwd := t.TempDir()
	touchTool(t, cwd)
	t.Chdir(cwd)
	ref, err := safety.ResolveProgram("tool", cwd, []string{"PATH=.", "PATHEXT=.CMD"})
	if err == nil || ref.OK {
		t.Fatalf("a relative PATH entry found %+v", ref)
	}
}

func TestResolveProgramRefusesSeparators(t *testing.T) {
	cwd := t.TempDir()
	touchTool(t, cwd)
	for _, name := range []string{"./tool", "sub/tool", `sub\tool`, filepath.Join(cwd, "tool"), ""} {
		ref, err := safety.ResolveProgram(name, cwd, []string{"PATH=" + cwd})
		if err == nil || ref.OK {
			t.Fatalf("%q resolved: %+v", name, ref)
		}
		if ref.Raw != name {
			t.Fatalf("Raw = %q", ref.Raw)
		}
	}
}

func TestResolveProgramCmdWrapper(t *testing.T) {
	pathDir := t.TempDir()
	touchTool(t, pathDir)
	ref, err := safety.ResolveProgram("tool.cmd", t.TempDir(), []string{"PATH=" + pathDir, "PATHEXT=.CMD"})
	if err != nil || !strings.HasPrefix(strings.ToLower(filepath.Base(ref.Canon)), "tool") {
		t.Fatalf("ref = %+v, %v", ref, err)
	}
}
