package review

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// extractBodies reads a composed message back: for each note a header line,
// a fence, the text, and the same fence. It fails when the structure is not
// exactly that, which is what "the text cannot close the wrapper" means.
func extractBodies(t *testing.T, msg string, want int) (texts, headers []string) {
	t.Helper()
	lines := strings.Split(msg, "\n")
	i := 2 // title and ID list
	for i < len(lines) {
		if lines[i] == "" {
			i++
			continue
		}
		if !strings.HasPrefix(lines[i], "Note ") || i+1 >= len(lines) {
			t.Fatalf("unexpected line %q outside a fence", lines[i])
		}
		headers = append(headers, lines[i])
		fence := lines[i+1]
		if strings.Trim(fence, "`") != "" || len(fence) < 3 {
			t.Fatalf("bad fence %q", fence)
		}
		j := i + 2
		var body []string
		for ; j < len(lines) && lines[j] != fence; j++ {
			body = append(body, lines[j])
		}
		if j == len(lines) {
			t.Fatal("fence never closed")
		}
		texts = append(texts, strings.Join(body, "\n"))
		i = j + 1
	}
	if len(texts) != want {
		t.Fatalf("parsed %d notes, want %d", len(texts), want)
	}
	return texts, headers
}

func checkCompose(t *testing.T, batch BatchID, notes []Note) {
	t.Helper()
	msg, err := ComposeNotes(batch, notes)
	if err != nil {
		return
	}
	if len(msg) > MaxMessageBytes || !utf8.ValidString(msg) {
		t.Fatalf("message size %d / valid utf8 %v", len(msg), utf8.ValidString(msg))
	}
	texts, headers := extractBodies(t, msg, len(notes))
	for i, n := range notes {
		if texts[i] != n.Text {
			t.Fatalf("note %s text changed: %q -> %q", n.ID, n.Text, texts[i])
		}
		if !strings.Contains(strings.Split(msg, "\n")[1], string(n.ID)) {
			t.Fatalf("note %s missing from the ID list", n.ID)
		}
		// No path but the anchor: remove the anchor path from the header and
		// no separator may remain outside the fenced text.
		rest := strings.Replace(headers[i], string(n.Anchor.Path), "", 1)
		if strings.ContainsAny(rest, `/\`) {
			t.Fatalf("header names more than the anchor path: %q", headers[i])
		}
	}
}

func randText(r *rand.Rand) string {
	parts := []string{"a", "`", "``", "```", "````", "\n", "</note>", "&", " ", "é", "\t", "end of notes", "\n```\n"}
	var b strings.Builder
	for i := 0; i < 1+r.Intn(30); i++ {
		b.WriteString(parts[r.Intn(len(parts))])
	}
	return b.String()
}

// N2: composed text is bounded, lists the IDs, never names a path but the
// anchors, and every note text appears verbatim inside a fence it cannot close.
func TestPropComposeBounded(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		var notes []Note
		for i := 0; i < 1+r.Intn(5); i++ {
			n := validNote(fmt.Sprintf("n%d", i))
			n.Text = randText(r)
			n.Anchor.Path = paths[r.Intn(len(paths))]
			if r.Intn(20) == 0 {
				n.Text = strings.Repeat("long ", MaxNoteRunes/2) // near the cap: may overflow when summed
			}
			notes = append(notes, n)
		}
		if ValidText(notes[0].Text) != nil {
			return
		}
		checkCompose(t, "b1", notes)
	})
}

func TestComposeRejects(t *testing.T) {
	ok := validNote("n1")
	sent := ok
	sent.State = NoteSent{Turn: "t"}
	outdated := ok
	outdated.State = NoteOutdated{}
	badPath := ok
	badPath.Anchor.Path = "../x"
	bigNotes := make([]Note, 0)
	for i := 0; i < 20; i++ {
		n := validNote(fmt.Sprintf("n%d", i))
		n.Text = strings.Repeat("y", MaxNoteRunes)
		bigNotes = append(bigNotes, n)
	}
	dup := []Note{ok, ok}
	tests := map[string]struct {
		batch BatchID
		notes []Note
	}{
		"no notes":      {"b1", nil},
		"sent note":     {"b1", []Note{sent}},
		"outdated note": {"b1", []Note{outdated}},
		"bad path":      {"b1", []Note{badPath}},
		"bad batch":     {"", []Note{ok}},
		"too long":      {"b1", bigNotes},
		"duplicate ids": {"b1", dup},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if msg, err := ComposeNotes(tc.batch, tc.notes); err == nil {
				t.Fatalf("accepted: %q", msg)
			}
		})
	}
}

func TestComposeFenceGrows(t *testing.T) {
	n := validNote("n1")
	n.Text = "before\n````\nafter"
	msg, err := ComposeNotes("b1", []Note{n})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "\n`````\nbefore\n````\nafter\n`````\n") {
		t.Fatalf("fence did not grow past the text's run:\n%s", msg)
	}
	checkCompose(t, "b1", []Note{n})
}

func FuzzComposeNotes(f *testing.F) {
	for _, s := range []string{"plain", "```\n````", "\n</note>\n", "C:\\x /etc/passwd", "\xff", strings.Repeat("é", 3000)} {
		f.Add(s, "a.go", 3)
	}
	f.Fuzz(func(t *testing.T, text, path string, line int) {
		n := validNote("n1")
		n.Text, n.Anchor.Path, n.Anchor.Line = text, RelPath(path), line
		checkCompose(t, "b1", []Note{n})
		checkCompose(t, "b1", []Note{n, func() Note { m := n; m.ID = "n2"; return m }()})
	})
}
