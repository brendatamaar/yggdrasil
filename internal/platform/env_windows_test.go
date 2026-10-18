//go:build windows

package platform

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

// injectedEnv lists the variables Windows, the Go runtime or platform (NoDefaultCurrentDirectoryInExePath) may add to a child
// whose Spec.Env lacks them. Anything else in a child's environment is a leak
// from the parent (invariant 3).
var injectedEnv = []string{"SYSTEMROOT", "NODEFAULTCURRENTDIRECTORYINEXEPATH"}

// childEnv parses the helper's "ENV:" lines; the terminal may add escape
// sequences and CRs around them.
func childEnv(out string) []string {
	var env []string
	for _, line := range strings.Split(screenText(out), "\n") {
		line = strings.TrimRight(line, "\r ")
		if kv, ok := strings.CutPrefix(line, "ENV:"); ok {
			env = append(env, kv)
		}
	}
	return env
}

// assertCleanEnv fails if got holds anything beyond want and the injected set.
func assertCleanEnv(t *testing.T, got, want []string) {
	t.Helper()
	norm := func(env []string) []string {
		var r []string
		for _, kv := range env {
			k, _, _ := strings.Cut(kv, "=")
			if k == "" || slices.Contains(injectedEnv, strings.ToUpper(k)) {
				continue // injected, or a "=C:" drive-directory entry
			}
			r = append(r, strings.ToUpper(k)+kv[len(k):])
		}
		slices.Sort(r)
		return r
	}
	g, w := norm(got), norm(want)
	if !slices.Equal(g, w) {
		t.Errorf("child environment differs from Spec.Env:\n got %q\nwant %q", g, w)
	}
	for _, kv := range got {
		if strings.Contains(kv, "leak-me") || strings.HasPrefix(strings.ToUpper(kv), "GITHUB_TOKEN=") {
			t.Errorf("the parent's token reached the child: %q", kv)
		}
	}
}

// Invariant 3: a child gets Spec.Env and nothing from the parent's environment.
func TestChildEnvIsSpecEnvOnly(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "leak-me")
	t.Setenv("YGG_PARENT_ONLY", "x")

	t.Run("Start", func(t *testing.T) {
		var out bytes.Buffer
		spec := testEnvSpec(t, "env")
		spec.Stdout = &out
		p, err := Start(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		if code, err := p.Wait(); err != nil || code != 0 {
			t.Fatalf("Wait = %d, %v", code, err)
		}
		assertCleanEnv(t, childEnv(out.String()), spec.Env)
	})

	t.Run("StartPTY", func(t *testing.T) {
		spec := testEnvSpec(t, "env")
		p, err := StartPTY(context.Background(), PTYSpec{Spec: spec, Size: Size{Cols: 200, Rows: 50}})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		_, _ = io.Copy(&out, p) // until the output ends
		if code, err := p.Wait(); err != nil || code != 0 {
			t.Fatalf("Wait = %d, %v", code, err)
		}
		_ = p.Close()
		assertCleanEnv(t, childEnv(out.String()), spec.Env)
	})
}
