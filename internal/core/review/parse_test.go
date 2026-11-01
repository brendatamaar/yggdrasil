package review

import (
	"strings"
	"testing"
)

func rawOK() RawNote {
	return RawNote{ID: "n1", Task: "t1", Repo: "r1", Path: "src/App.java", Line: 12, Side: "new", Text: "rename", DiffHash: hash("c")}
}

func TestParseNoteAccepts(t *testing.T) {
	r := rawOK()
	r.Text = "line1\r\nline2\ttab"
	n, err := ParseNote(r, epoch)
	if err != nil {
		t.Fatal(err)
	}
	if n.Text != "line1\nline2\ttab" {
		t.Fatalf("text = %q", n.Text)
	}
	if _, open := n.State.(NoteOpen); !open || n.Created != epoch {
		t.Fatalf("state %T created %v", n.State, n.Created)
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("accepted but not valid: %v", err)
	}
}

func TestParseNoteRejects(t *testing.T) {
	tests := map[string]func(*RawNote){
		"absolute path":       func(r *RawNote) { r.Path = "/etc/passwd" },
		"parent segment":      func(r *RawNote) { r.Path = "a/../../b" },
		"drive":               func(r *RawNote) { r.Path = "C:/x" },
		"stream":              func(r *RawNote) { r.Path = "a.txt:s" },
		"extended prefix":     func(r *RawNote) { r.Path = `\\?\C:\x` },
		"backslash":           func(r *RawNote) { r.Path = `a\b` },
		"device name":         func(r *RawNote) { r.Path = "dir/NUL.txt" },
		"com port":            func(r *RawNote) { r.Path = "com1" },
		"short name":          func(r *RawNote) { r.Path = "PROGRA~1/x" },
		"trailing dot":        func(r *RawNote) { r.Path = "a./b" },
		"empty path":          func(r *RawNote) { r.Path = "" },
		"line zero":           func(r *RawNote) { r.Line = 0 },
		"line over the cap":   func(r *RawNote) { r.Line = MaxLine + 1 },
		"unknown side":        func(r *RawNote) { r.Side = "left" },
		"empty text":          func(r *RawNote) { r.Text = "  \n" },
		"long text":           func(r *RawNote) { r.Text = strings.Repeat("x", MaxNoteRunes+1) },
		"control character":   func(r *RawNote) { r.Text = "a\x1bb" },
		"lone carriage ret":   func(r *RawNote) { r.Text = "a\rb" },
		"bad hash":            func(r *RawNote) { r.DiffHash = "abc" },
		"id with a space":     func(r *RawNote) { r.ID = "n 1" },
		"repo with a control": func(r *RawNote) { r.Repo = "r\x00" },
	}
	for name, mut := range tests {
		t.Run(name, func(t *testing.T) {
			r := rawOK()
			mut(&r)
			if n, err := ParseNote(r, epoch); err == nil {
				t.Fatalf("accepted: %+v", n)
			}
		})
	}
}

func FuzzParseNote(f *testing.F) {
	f.Add("n1", "t1", "r1", "a.go", 3, "new", "text", hash("a"))
	f.Add("n1", "t1", "r1", `..\x`, 0, "", "\x00", "zz")
	f.Add("", "", "", "con", -1, "old", "a\r\nb", hash("0"))
	f.Fuzz(func(t *testing.T, id, task, repo, path string, line int, side, text, dh string) {
		n, err := ParseNote(RawNote{ID: id, Task: task, Repo: repo, Path: path, Line: line, Side: side, Text: text, DiffHash: dh}, epoch)
		if err != nil {
			return
		}
		if verr := n.Validate(); verr != nil {
			t.Fatalf("accepted but invalid: %v", verr)
		}
		if strings.HasPrefix(string(n.Anchor.Path), "/") || strings.ContainsAny(string(n.Anchor.Path), `\:`) || strings.Contains(string(n.Anchor.Path), "..") && strings.Contains("/"+string(n.Anchor.Path)+"/", "/../") {
			t.Fatalf("path escapes the repo: %q", n.Anchor.Path)
		}
	})
}
