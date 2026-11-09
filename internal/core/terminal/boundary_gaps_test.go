package terminal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Gap tests for the boundary parsers of docs/design/terminal.md.

// Round trip: every canonical 32-byte token parses, and its hash is the hash
// of exactly that string (the core never sees anything else of the token).
func TestParseTokenFormatRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		var b [32]byte
		r.Read(b[:])
		raw := base64.RawURLEncoding.EncodeToString(b[:])
		h, err := ParseToken(raw)
		if err != nil || h != HashToken(raw) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
}

// Exactly the 16 canonical last characters are accepted; the other 48 carry
// stray bits and are refused (one rejection rule, checked exhaustively).
func TestParseTokenLastCharacterIsCanonical(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	prefix := strings.Repeat("A", TokenLen-1)
	var accepted int
	for _, c := range alphabet {
		raw := prefix + string(c)
		_, err := ParseToken(raw)
		_, strictErr := base64.RawURLEncoding.Strict().DecodeString(raw)
		if (err == nil) != (strictErr == nil) {
			t.Errorf("last char %q: ParseToken err %v, strict decoder err %v", c, err, strictErr)
		}
		if err == nil {
			accepted++
		}
	}
	if accepted != 16 {
		t.Errorf("%d last characters accepted, want 16", accepted)
	}
}

// Different tokens never share a hash; the same token always does.
func TestHashTokenIsDeterministicAndSeparates(t *testing.T) {
	a, b := strings.Repeat("A", TokenLen), strings.Repeat("A", TokenLen-1)+"E"
	if HashToken(a) != HashToken(a) || HashToken(a) == HashToken(b) {
		t.Fatal("hash is not a function of the token")
	}
	if strings.Contains(string(HashToken(a)), a) {
		t.Fatal("the hash contains the raw token")
	}
}

// ParseIssue(format(x)) == x for every valid target; invalid ones never
// format into something accepted.
func TestParseIssueRoundTrip(t *testing.T) {
	ids := []string{"a", "repo_9f86d081", "r.1-x_y", "A", strings.Repeat("z", 128)}
	for _, id := range ids {
		for _, key := range []string{"repoId", "worktreeId"} {
			body, _ := json.Marshal(map[string]string{key: id})
			got, err := ParseIssue(body)
			if err != nil {
				t.Fatalf("%s: %v", body, err)
			}
			if (key == "repoId" && string(got.Repo) != id) || (key == "worktreeId" && string(got.Worktree) != id) {
				t.Fatalf("%s -> %+v", body, got)
			}
			if got.Validate() != nil {
				t.Fatalf("accepted but invalid: %+v", got)
			}
		}
	}
	// Characters outside the ID alphabet are each rejected.
	for _, c := range []string{"/", "\\", ":", " ", "\x00", "é", "%", "~", "$", "*"} {
		body, _ := json.Marshal(map[string]string{"repoId": "a" + c + "b"})
		if _, err := ParseIssue(body); err == nil {
			t.Errorf("id with %q accepted", c)
		}
	}
	if _, err := ParseIssue([]byte(`{"repoId":"` + strings.Repeat("a", 128) + `"}`)); err != nil {
		t.Errorf("an id of exactly 128 characters: %v", err)
	}
}

// ParseFrame(format(frame)) == frame for resize and data frames over the whole
// accepted range edges, and anything outside never parses.
func TestParseFrameRoundTrip(t *testing.T) {
	for _, s := range []Size{{1, 1}, {500, 500}, {80, 24}, {1, 500}} {
		payload, _ := json.Marshal(map[string]any{"type": "resize", "cols": s.Cols, "rows": s.Rows})
		f, err := ParseFrame(TextFrame, payload)
		if err != nil || f != (ResizeFrame{Size: s}) {
			t.Fatalf("%s: %v %v", payload, f, err)
		}
	}
	for _, d := range [][]byte{[]byte("a"), {0x03}, []byte("\x1b[A"), []byte("echo é\r\n"), make([]byte, MaxFrameBytes)} {
		f, err := ParseFrame(BinaryFrame, d)
		if err != nil || string(f.(DataFrame).Data) != string(d) {
			t.Fatalf("data %q: %v", d, err)
		}
	}
	for _, c := range []int{-1, 0, 501, 1 << 20} {
		for _, p := range []string{
			fmt.Sprintf(`{"type":"resize","cols":%d,"rows":10}`, c),
			fmt.Sprintf(`{"type":"resize","cols":10,"rows":%d}`, c),
		} {
			if f, err := ParseFrame(TextFrame, []byte(p)); err == nil {
				t.Errorf("%s accepted as %v", p, f)
			}
		}
	}
}

// A text frame is only a resize: a data-looking JSON never becomes input.
func TestParseFrameTextNeverCarriesInput(t *testing.T) {
	for _, p := range []string{`{"type":"input","data":"ls"}`, `{"type":"resize","cols":1,"rows":1,"data":"ls"}`, `"ls"`, `null`, `{}`} {
		if f, err := ParseFrame(TextFrame, []byte(p)); err == nil {
			t.Errorf("%s accepted as %#v", p, f)
		}
	}
}

func FuzzParseTokenHashIsHex(f *testing.F) {
	f.Add(strings.Repeat("A", TokenLen))
	f.Fuzz(func(t *testing.T, s string) {
		h, err := ParseToken(s)
		if err == nil && (h.Validate() != nil || strings.Contains(string(h), s)) {
			t.Fatalf("bad hash for %q", s)
		}
	})
}
