//go:build windows

package platform

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWithNoCurDir(t *testing.T) {
	in := []string{"A=1", "nodefaultcurrentdirectoryinexepath=0", "B=2"}
	orig := slices.Clone(in)
	out := withNoCurDir(in)
	if !slices.Equal(in, orig) {
		t.Errorf("the caller's slice changed: %q", in)
	}
	want := []string{"A=1", "B=2", "NoDefaultCurrentDirectoryInExePath=1"}
	if !slices.Equal(out, want) {
		t.Errorf("withNoCurDir = %q, want %q", out, want)
	}
}

// noCurDirFixture builds: a batch file that calls a bare "helper"; a real
// helper.cmd on the env PATH; and a helper.cmd planted in the working dir that
// writes a marker file.
func noCurDirFixture(t *testing.T) (batch, workDir, marker string, env []string) {
	t.Helper()
	root := t.TempDir()
	mk := func(parts ...string) string {
		d := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		return d
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shimDir, workDir, binDir := mk("shim dir"), mk("worktree"), mk("bin")
	marker = filepath.Join(root, "planted-ran.txt")
	batch = filepath.Join(shimDir, "shim.cmd")
	write(batch, "@echo off\r\nhelper\r\n")
	write(filepath.Join(binDir, "helper.cmd"), "@echo off\r\necho REALHELPER\r\n")
	write(filepath.Join(workDir, "helper.cmd"), "@echo off\r\necho x> \""+marker+"\"\r\n")
	env = append(helperEnv(), "COMSPEC="+os.Getenv("COMSPEC"))
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + binDir + ";" + strings.TrimPrefix(kv, "PATH=")
		}
	}
	return batch, workDir, marker, env
}

// A command inside a batch file is looked up on PATH, never in the working
// directory (invariant 5: a file an agent writes into its worktree must not
// run). To see this test fail, remove the withNoCurDir calls in start_windows.go
// and pty_windows.go: the planted helper.cmd runs and the marker appears.
func TestBatchFileDoesNotRunPlantedCommandFromDir(t *testing.T) {
	t.Run("Start", func(t *testing.T) {
		batch, dir, marker, env := noCurDirFixture(t)
		var out bytes.Buffer
		p, err := Start(context.Background(), Spec{Argv: []string{batch}, Dir: dir, Env: env, Stdout: &out, Stderr: &out})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Wait(); err != nil {
			t.Fatal(err)
		}
		assertNotPlanted(t, marker, out.String())
	})
	t.Run("StartPTY", func(t *testing.T) {
		batch, dir, marker, env := noCurDirFixture(t)
		p, err := StartPTY(context.Background(), PTYSpec{Spec: Spec{Argv: []string{batch}, Dir: dir, Env: env}, Size: Size{Cols: 120, Rows: 30}})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		_, _ = io.Copy(&out, p)
		if _, err := p.Wait(); err != nil {
			t.Fatal(err)
		}
		_ = p.Close()
		assertNotPlanted(t, marker, out.String())
	})
}

func assertNotPlanted(t *testing.T, marker, out string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Error("the helper.cmd planted in the working directory ran")
	}
	if !strings.Contains(out, "REALHELPER") {
		t.Errorf("the helper on PATH did not run; output: %q", out)
	}
}
