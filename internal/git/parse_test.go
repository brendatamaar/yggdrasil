package git

import (
	"slices"
	"testing"
)

// Parsers run on fixture text, so they are tested without git.

func TestParseStatusFixture(t *testing.T) {
	const h = "# branch.oid 1111111111111111111111111111111111111111\x00" +
		"# branch.head feature/x\x00# branch.upstream origin/feature/x\x00# branch.ab +2 -3\x00"
	const sha = "1111111111111111111111111111111111111111"
	out := h +
		"1 .M N... 100644 100644 100644 " + sha + " " + sha + " dir with space/a b.txt\x00" +
		"1 M. N... 100644 100644 100644 " + sha + " " + sha + " crlf\r.txt\x00" +
		"2 R. N... 100644 100644 100644 " + sha + " " + sha + " R100 new name.txt\x00old name.txt\x00" +
		"u UU N... 100644 100644 100644 100644 " + sha + " " + sha + " " + sha + " conflict.txt\x00" +
		"? caf\u00e9 \u2713.txt\x00! ignored.log\x00"
	st, err := parseStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "feature/x" || st.Upstream != "origin/feature/x" || !st.HasUpstream || st.Ahead != 2 || st.Behind != 3 {
		t.Errorf("header: %+v", st)
	}
	if st.Staged != 2 || st.Unstaged != 1 || st.Untracked != 1 || st.Conflicted != 1 {
		t.Errorf("counts: %+v", st)
	}
	paths := []string{}
	for _, f := range st.Files {
		paths = append(paths, f.Path)
	}
	want := []string{"dir with space/a b.txt", "crlf\r.txt", "new name.txt", "conflict.txt", "caf\u00e9 \u2713.txt"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}
	if st.Files[2].OrigPath != "old name.txt" || st.Files[2].Index != "R" || st.Files[2].Worktree != "" {
		t.Errorf("rename: %+v", st.Files[2])
	}
	if !st.Dirty() {
		t.Error("Dirty() = false")
	}
}

func TestParseStatusDetachedInitial(t *testing.T) {
	st, err := parseStatus("# branch.oid (initial)\x00# branch.head (detached)\x00")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Detached || st.Branch != "" || st.OID != "" || st.HasUpstream {
		t.Errorf("%+v", st)
	}
}

func TestParseStatusRejectsGarbage(t *testing.T) {
	for _, in := range []string{"1 .M short\x00", "2 R. a b\x00", "zzz\x00"} {
		if _, err := parseStatus(in); err == nil {
			t.Errorf("parseStatus(%q) = nil error", in)
		}
	}
}

func TestParseWorktrees(t *testing.T) {
	out := "worktree C:/code/shop api\r\nHEAD aaaa\r\nbranch refs/heads/main\r\n\r\n" +
		"worktree C:/wt/x\nHEAD bbbb\ndetached\nlocked reason\n\nworktree C:/bare\nbare\n"
	got := parseWorktrees(out)
	if len(got) != 3 {
		t.Fatalf("got %d worktrees: %+v", len(got), got)
	}
	if got[0].Branch != "main" || got[0].Head != "aaaa" {
		t.Errorf("first: %+v", got[0])
	}
	if !got[1].Detached || !got[1].Locked || got[1].Branch != "" {
		t.Errorf("second: %+v", got[1])
	}
	if !got[2].Bare {
		t.Errorf("third: %+v", got[2])
	}
}
