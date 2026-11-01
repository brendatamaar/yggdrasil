package review

import (
	"strings"
	"testing"
	"unicode/utf8"
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

// noteTokenOK is an independent check of an id/repo/task: non-empty, none of
// the separators or whitespace the ID rules refuse, no control rune, no
// Unicode line or paragraph separator.
func noteTokenOK(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n/\\") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 {
			return false
		}
	}
	return true
}

// notePathOK is an independent check of a repo-relative note path.
func notePathOK(p string) bool {
	if p == "" || len(p) > MaxPathBytes || !utf8.ValidString(p) || strings.HasPrefix(p, "/") ||
		strings.ContainsAny(p, `\:`) {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
			return false
		}
	}
	return true
}

// noteTextOK is an independent check of normalised note text.
func noteTextOK(s string) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > MaxNoteRunes {
		return false
	}
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return false
		}
	}
	return true
}

func noteHashOK(h string) bool {
	if len(h) != 64 {
		return false
	}
	for i := 0; i < len(h); i++ {
		if !(h[i] >= '0' && h[i] <= '9' || h[i] >= 'a' && h[i] <= 'f') {
			return false
		}
	}
	return true
}

func TestParseNoteRejectsMore(t *testing.T) {
	tests := map[string]func(*RawNote){
		"empty id":           func(r *RawNote) { r.ID = "" },
		"empty task":         func(r *RawNote) { r.Task = "" },
		"task with a space":  func(r *RawNote) { r.Task = "t 1" },
		"task with a slash":  func(r *RawNote) { r.Task = "t/1" },
		"trailing space":     func(r *RawNote) { r.Path = "dir/file " },
		"dot-only segment":   func(r *RawNote) { r.Path = "dir/./x" },
		"dots-only name":     func(r *RawNote) { r.Path = "dir/.../x" },
		"single dot":         func(r *RawNote) { r.Path = "." },
		"double dot":         func(r *RawNote) { r.Path = ".." },
		"trailing space dir": func(r *RawNote) { r.Path = "dir /x" },
		"line separator id":  func(r *RawNote) { r.ID = "a b" },
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

// KNOWN BUG (fails on purpose): ParseNote applies plainToken to the note id
// and repo but not to the task, and core.TaskID.Validate only refuses ASCII
// whitespace and slashes. A task id such as "\x00" or "a b" is accepted.
func TestBugParseNoteTaskControlChars(t *testing.T) {
	for _, task := range []string{"\x00", "t\x1b1", "a b"} {
		r := rawOK()
		r.Task = task
		if n, err := ParseNote(r, epoch); err == nil {
			t.Errorf("task %q accepted: %+v", task, n.Task)
		}
	}
}

func TestParseNoteNormalisationIdempotent(t *testing.T) {
	r := rawOK()
	r.Text = "a\r\nb\r\n\r\nc"
	n, err := ParseNote(r, epoch)
	if err != nil {
		t.Fatal(err)
	}
	r.Text = n.Text
	m, err := ParseNote(r, epoch)
	if err != nil || m.Text != n.Text {
		t.Fatalf("second parse %q, %v", m.Text, err)
	}
}

func FuzzParseNote(f *testing.F) {
	f.Add("n1", "t1", "r1", "a.go", 3, "new", "text", hash("a"))
	f.Add("n1", "t1", "r1", `..\x`, 0, "", "\x00", "zz")
	f.Add("", "", "", "con", -1, "old", "a\r\nb", hash("0"))
	f.Add("n1", "t1", "r1", "a /b", 1, "old", "x\r\n\r\ny", hash("f"))
	f.Fuzz(func(t *testing.T, id, task, repo, path string, line int, side, text, dh string) {
		raw := RawNote{ID: id, Task: task, Repo: repo, Path: path, Line: line, Side: side, Text: text, DiffHash: dh}
		n, err := ParseNote(raw, epoch)
		if err != nil {
			return
		}
		// Accepted implies valid, by a predicate that does not call Validate.
		switch {
		case !noteTokenOK(id) || !noteTokenOK(repo):
			t.Fatalf("accepted bad id/repo: %q %q", id, repo)
		case task == "" || strings.ContainsAny(task, " \t\r\n/\\"):
			// Control characters in task are accepted: see TestBugParseNoteTaskControlChars.
			t.Fatalf("accepted bad task %q", task)
		case !notePathOK(path):
			t.Fatalf("accepted bad path %q", path)
		case line < 1 || line > MaxLine:
			t.Fatalf("accepted line %d", line)
		case side != "old" && side != "new":
			t.Fatalf("accepted side %q", side)
		case !noteTextOK(n.Text) || strings.Contains(n.Text, "\r\n"):
			t.Fatalf("accepted or normalised bad text %q", n.Text)
		case !noteHashOK(dh):
			t.Fatalf("accepted hash %q", dh)
		}
		if n.Text != strings.ReplaceAll(text, "\r\n", "\n") {
			t.Fatalf("text is more than CRLF-normalised: %q from %q", n.Text, text)
		}
		// Idempotent: parsing the normalised text gives the same note.
		raw.Text = n.Text
		m, err := ParseNote(raw, epoch)
		if err != nil || m.Text != n.Text || m.Anchor != n.Anchor || m.ID != n.ID {
			t.Fatalf("not idempotent: %+v, %v", m, err)
		}
	})
}
