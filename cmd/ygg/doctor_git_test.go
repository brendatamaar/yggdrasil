package main

import (
	"errors"
	"strings"
	"testing"
)

func TestGitMergeTreeAdvice(t *testing.T) {
	ok := []string{"git version 2.38.0", "git version 2.43.0.windows.1", "git version 2.50.1", "git version 3.0.0"}
	for _, l := range ok {
		if err := gitMergeTreeAdvice(l); err != nil {
			t.Errorf("%q: %v", l, err)
		}
	}
	old := []string{"git version 2.37.9", "git version 2.30.0.windows.2", "git version 1.9.5"}
	for _, l := range old {
		err := gitMergeTreeAdvice(l)
		var a advice
		if err == nil || !errors.As(err, &a) || !strings.Contains(err.Error(), "slower merge-then-abort") || !strings.Contains(err.Error(), "Update Git") {
			t.Errorf("%q: %v", l, err)
		}
	}
	if err := gitMergeTreeAdvice("garbage"); err == nil || !strings.Contains(err.Error(), "still work") {
		t.Errorf("unparseable: %v", err)
	}
}

func TestGitMergeTreeProbeIsNeverRequired(t *testing.T) {
	if gitMergeTreeProbe(nil).required {
		t.Fatal("an old Git is a warning, not a failure")
	}
	var found bool
	for _, p := range defaultProbes(config{dataDir: t.TempDir()}, nil) {
		if p.name == "git merge-tree" {
			found = true
		}
	}
	if !found {
		t.Fatal("no git merge-tree probe in the defaults")
	}
}
