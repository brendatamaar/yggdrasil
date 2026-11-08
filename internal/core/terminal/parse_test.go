package terminal

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func newRaw(t testing.TB) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func TestParseTokenRoundTrip(t *testing.T) {
	for i := 0; i < 500; i++ {
		raw := newRaw(t)
		h, err := ParseToken(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if h != HashToken(raw) {
			t.Fatal("hash differs from HashToken")
		}
		if err := h.Validate(); err != nil {
			t.Fatalf("accepted token gave an invalid hash: %v", err)
		}
	}
}

func TestParseTokenRejects(t *testing.T) {
	good := newRaw(t)
	cases := map[string]string{
		"empty":        "",
		"too short":    good[:42],
		"too long":     good + "A",
		"padding":      good[:42] + "=",
		"standard b64": good[:10] + "+" + good[11:],
		"slash":        good[:10] + "/" + good[11:],
		"space":        good[:10] + " " + good[11:],
		"non ascii":    good[:10] + "é" + good[12:],
		"stray bits":   good[:42] + "B",
		"path":         "../../" + good[6:],
	}
	for name, in := range cases {
		if _, err := ParseToken(in); err == nil {
			t.Errorf("%s: %q accepted", name, in)
		}
	}
}

func FuzzParseToken(f *testing.F) {
	f.Add(strings.Repeat("A", 43))
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		h, err := ParseToken(s)
		if err != nil {
			return
		}
		if h.Validate() != nil {
			t.Fatalf("accepted %q with invalid hash", s)
		}
		b, err := base64.RawURLEncoding.Strict().DecodeString(s)
		if err != nil || len(b) != 32 {
			t.Fatalf("accepted %q, which is not a canonical 32-byte token", s)
		}
	})
}

func TestTokenHashValidate(t *testing.T) {
	for _, bad := range []TokenHash{"", "abc", TokenHash(strings.Repeat("A", 64)), TokenHash(strings.Repeat("g", 64)), TokenHash(strings.Repeat("a", 65))} {
		if bad.Validate() == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if HashToken("x").Validate() != nil {
		t.Error("HashToken output invalid")
	}
}

func TestParseIssueAccepts(t *testing.T) {
	for body, want := range map[string]Target{
		`{"repoId":"repo_9f86d081"}`:         {Repo: "repo_9f86d081"},
		`{"worktreeId":"wt_1"}`:              {Worktree: "wt_1"},
		` {"repoId":"r.1-x_y"} `:             {Repo: "r.1-x_y"},
		`{"repoId":"a","worktreeId":""}`:     {Repo: "a"},
		`{"repoId":"","worktreeId":"w_a_1"}`: {Worktree: "w_a_1"},
	} {
		got, err := ParseIssue([]byte(body))
		if err != nil || got != want {
			t.Errorf("%s: %v, %v", body, got, err)
		}
		if got.Validate() != nil {
			t.Errorf("accepted %s but invalid", body)
		}
	}
}

func TestParseIssueRejects(t *testing.T) {
	cases := map[string]string{
		"both":        `{"repoId":"a","worktreeId":"b"}`,
		"neither":     `{}`,
		"empty ids":   `{"repoId":"","worktreeId":""}`,
		"path field":  `{"repoId":"a","path":"C:\\x"}`,
		"dir field":   `{"dir":"/etc"}`,
		"slash in id": `{"repoId":"a/b"}`,
		"backslash":   `{"repoId":"a\\b"}`,
		"dotdot":      `{"repoId":"a..b"}`,
		"leading dot": `{"repoId":".a"}`,
		"drive":       `{"repoId":"C:"}`,
		"space":       `{"worktreeId":"a b"}`,
		"long":        `{"repoId":"` + strings.Repeat("a", 129) + `"}`,
		"not json":    `repo`,
		"array":       `[]`,
		"trailing":    `{"repoId":"a"}{"repoId":"b"}`,
		"number":      `{"repoId":5}`,
		"empty body":  ``,
	}
	for name, body := range cases {
		if got, err := ParseIssue([]byte(body)); err == nil {
			t.Errorf("%s: accepted as %v", name, got)
		}
	}
}

func TestParseResize(t *testing.T) {
	for _, c := range [][2]int{{1, 1}, {500, 500}, {80, 24}} {
		if s, err := ParseResize(c[0], c[1]); err != nil || s.Validate() != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	for _, c := range [][2]int{{0, 1}, {1, 0}, {501, 1}, {1, 501}, {-1, 5}} {
		if _, err := ParseResize(c[0], c[1]); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
}

func TestParseFrameAccepts(t *testing.T) {
	f, err := ParseFrame(BinaryFrame, []byte("ls\r"))
	if d, ok := f.(DataFrame); err != nil || !ok || string(d.Data) != "ls\r" {
		t.Fatalf("data: %v %v", f, err)
	}
	f, err = ParseFrame(TextFrame, []byte(`{"type":"resize","cols":120,"rows":30}`))
	if r, ok := f.(ResizeFrame); err != nil || !ok || r.Size != (Size{120, 30}) {
		t.Fatalf("resize: %v %v", f, err)
	}
	if _, err := ParseFrame(BinaryFrame, make([]byte, MaxFrameBytes)); err != nil {
		t.Errorf("a frame of exactly the limit: %v", err)
	}
}

func TestParseFrameDataIsCopied(t *testing.T) {
	in := []byte("abc")
	f, _ := ParseFrame(BinaryFrame, in)
	in[0] = 'X'
	if string(f.(DataFrame).Data) != "abc" {
		t.Error("frame shares the caller's buffer")
	}
}

func TestParseFrameRejects(t *testing.T) {
	type c struct {
		kind FrameKind
		body string
	}
	cases := map[string]c{
		"empty binary":    {BinaryFrame, ""},
		"oversize binary": {BinaryFrame, strings.Repeat("a", MaxFrameBytes+1)},
		"oversize text":   {TextFrame, `{"type":"resize","cols":1,"rows":1}` + strings.Repeat(" ", MaxFrameBytes)},
		"other type":      {TextFrame, `{"type":"exec","cols":1,"rows":1}`},
		"missing type":    {TextFrame, `{"cols":1,"rows":1}`},
		"unknown field":   {TextFrame, `{"type":"resize","cols":1,"rows":1,"cmd":"x"}`},
		"cols zero":       {TextFrame, `{"type":"resize","cols":0,"rows":1}`},
		"rows 501":        {TextFrame, `{"type":"resize","cols":1,"rows":501}`},
		"cols missing":    {TextFrame, `{"type":"resize","rows":10}`},
		"plain text":      {TextFrame, `ls`},
		"trailing":        {TextFrame, `{"type":"resize","cols":1,"rows":1} x`},
		"float":           {TextFrame, `{"type":"resize","cols":1.5,"rows":1}`},
		"unknown kind":    {FrameKind(9), "x"},
	}
	for name, tc := range cases {
		if f, err := ParseFrame(tc.kind, []byte(tc.body)); err == nil {
			t.Errorf("%s: accepted as %v", name, f)
		}
	}
}

func FuzzParseFrame(f *testing.F) {
	f.Add(uint8(1), []byte(`{"type":"resize","cols":80,"rows":24}`))
	f.Add(uint8(2), []byte("ls\r"))
	f.Add(uint8(1), []byte(`{}`))
	f.Fuzz(func(t *testing.T, kind uint8, payload []byte) {
		fr, err := ParseFrame(FrameKind(kind), payload)
		if err != nil {
			return
		}
		if fr.Validate() != nil {
			t.Fatalf("accepted an invalid frame %#v", fr)
		}
		if len(payload) > MaxFrameBytes {
			t.Fatal("accepted an oversize frame")
		}
	})
}

func FuzzParseIssue(f *testing.F) {
	f.Add([]byte(`{"repoId":"a"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		tg, err := ParseIssue(body)
		if err == nil && tg.Validate() != nil {
			t.Fatalf("accepted invalid target %v", tg)
		}
	})
}
