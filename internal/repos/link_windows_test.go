//go:build windows

package repos_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// makeLink makes a directory junction (no privilege needed, unlike symlinks).
func makeLink(t *testing.T, link, target string) bool {
	t.Helper()
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: []string{filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "mklink", "/J", link, target},
		Env:  env, Stdout: &out, Stderr: &out,
	})
	if err != nil {
		t.Logf("start mklink: %v", err)
		return false
	}
	code, err := p.Wait()
	if err != nil || code != 0 {
		t.Logf("mklink /J failed: exit %d, %v: %s", code, err, out.String())
		return false
	}
	return true
}
