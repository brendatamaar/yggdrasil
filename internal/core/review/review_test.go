package review

import (
	"strings"
	"testing"
)

func TestRelPathValidate(t *testing.T) {
	for _, ok := range []string{"a.go", "src/main/App.java", "dir with space/f.txt"} {
		if err := RelPath(ok).Validate(); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../b", "C:/x", "a:stream", `a\b`, "a//b", "./a", "a\nb"} {
		if RelPath(bad).Validate() == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNoteValidate(t *testing.T) {
	n := Note{ID: "n1", Task: "t", Repo: "r", Anchor: Anchor{Path: "a.go", Line: 3, Side: SideNew},
		Text: "rename this", DiffHash: strings.Repeat("a", 64), State: NoteOpen{}}
	if err := n.Validate(); err != nil {
		t.Fatal(err)
	}
	n.Anchor.Line = 0
	if n.Validate() == nil {
		t.Error("line 0 accepted")
	}
	n.Anchor.Line = 1
	n.Text = strings.Repeat("x", MaxNoteRunes+1)
	if n.Validate() == nil {
		t.Error("long text accepted")
	}
}
