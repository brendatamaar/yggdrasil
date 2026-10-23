package git

import (
	"slices"
	"strings"
	"testing"
)

func TestDiffLimitStopsAtTheCap(t *testing.T) {
	f := newFx(t)
	dir := f.repo("lim")
	f.commit(dir, "big.txt", "x\n", "init")
	f.write(dir, "big.txt", strings.Repeat("a long line of changed text\n", 20000)) // about 560 KB

	full, err := f.g.Diff(bg(), dir, DiffOptions{Mode: DiffWorktree})
	if err != nil || len(full) < 500_000 {
		t.Fatalf("uncapped diff = %d bytes, %v", len(full), err)
	}
	const limit = 1000
	d, err := f.g.Diff(bg(), dir, DiffOptions{Mode: DiffWorktree, Limit: limit})
	if err != nil {
		t.Fatalf("capped diff: %v", err)
	}
	if len(d) != limit+1 || d != full[:limit+1] {
		t.Errorf("capped diff = %d bytes, want %d (a prefix of the full diff)", len(d), limit+1)
	}
	small, err := f.g.Diff(bg(), dir, DiffOptions{Mode: DiffWorktree, Limit: len(full) + 10})
	if err != nil || small != full {
		t.Errorf("a diff below the cap must be returned whole: %d vs %d bytes, %v", len(small), len(full), err)
	}
}

func TestDiffPathIsLiteral(t *testing.T) {
	f := newFx(t)
	dir := f.repo("lit")
	f.commit(dir, "a.txt", "one\n", "init")
	f.commit(dir, "[a].txt", "one\n", "second")
	f.write(dir, "a.txt", "two\n")
	f.write(dir, "[a].txt", "two\n")

	d, err := f.g.Diff(bg(), dir, DiffOptions{Mode: DiffWorktree, Path: "[a].txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "[a].txt") || strings.Contains(d, "a/a.txt") {
		t.Errorf("a path with glob characters must match only itself:\n%s", d)
	}
}

func TestGitEnvForcesCLocaleAndNoPrompt(t *testing.T) {
	got := gitEnv([]string{"PATH=/bin", "LC_ALL=de_DE.UTF-8", "LANGUAGE=de", "GIT_TERMINAL_PROMPT=1"})
	for _, want := range []string{"PATH=/bin", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0"} {
		if !slices.Contains(got, want) {
			t.Errorf("env %v lacks %s", got, want)
		}
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "LANGUAGE=") || kv == "LC_ALL=de_DE.UTF-8" || kv == "GIT_TERMINAL_PROMPT=1" {
			t.Errorf("env keeps %s", kv)
		}
	}
}
