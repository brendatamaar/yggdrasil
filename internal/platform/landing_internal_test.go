package platform

import (
	"path/filepath"
	"testing"
)

func TestStagedInside(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "data", "worktrees")
	if filepath.VolumeName(`C:\`) != "" {
		root = `C:\data\worktrees`
	}
	j := func(p ...string) string { return filepath.Join(append([]string{root}, p...)...) }
	sep := string(filepath.Separator)
	for name, c := range map[string]struct {
		staged string
		want   bool
	}{
		"child":              {j(StagingDir, "wt-1"), true},
		"grandchild":         {j(StagingDir, "wt-1", "sub"), true},
		"the staging folder": {j(StagingDir), false},
		"the root":           {root, false},
		"parent":             {filepath.Dir(root), false},
		"sibling":            {j("other"), false},
		"similar name":       {j(StagingDir+"x", "a"), false},
		"dotdot out":         {j(StagingDir, "..", "other"), false},
		"dotdot raw":         {root + sep + StagingDir + sep + ".." + sep + "x", false},
		"relative":           {StagingDir + sep + "a", false},
		"empty":              {"", false},
		"nul":                {j(StagingDir, "a\x00b"), false},
		"fs root":            {filepath.VolumeName(root) + sep, false},
	} {
		if got := stagedInside(root, c.staged); got != c.want {
			t.Errorf("%s: stagedInside(%q) = %v, want %v", name, c.staged, got, c.want)
		}
	}
	if stagedInside(filepath.VolumeName(root)+sep, filepath.VolumeName(root)+sep+StagingDir+sep+"a") {
		t.Error("a drive or file system root is not a worktree root")
	}
}

func FuzzRemoveStagedPath(f *testing.F) {
	root := filepath.Join(string(filepath.Separator), "data", "worktrees")
	if filepath.VolumeName(`C:\`) != "" {
		root = `C:\data\worktrees`
	}
	for _, s := range []string{
		filepath.Join(root, StagingDir, "a"), root, filepath.Join(root, StagingDir),
		filepath.Join(root, StagingDir, "..", "x"), "", "..", filepath.Join(root, StagingDir) + "//a/../../b",
		root + `\.removing\..\x`, root + "/.REMOVING/a",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, staged string) {
		if !stagedInside(root, staged) {
			return
		}
		// Accepted means: no ".." part, inside root/.removing, one level below.
		rel, err := filepath.Rel(filepath.Join(root, StagingDir), filepath.Clean(staged))
		if err != nil || rel == "." || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) || filepath.IsAbs(rel) {
			t.Fatalf("accepted %q but its relative path is %q (%v)", staged, rel, err)
		}
		for _, r := range staged {
			if r == 0 {
				t.Fatalf("accepted %q with NUL", staged)
			}
		}
	})
}
