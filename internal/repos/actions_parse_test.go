package repos_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

func TestParseBatchRequestAccepts(t *testing.T) {
	cases := []struct {
		kind   string
		ids    []string
		branch string
	}{
		{"fetch", []string{"rep_a"}, ""},
		{"fetch", []string{"rep_a", "rep_b", "rep_c"}, ""},
		{"pull", []string{"rep_a", "rep_b"}, ""},
		{"checkout", []string{"rep_a"}, "feature/x"},
		{"switch", []string{"rep_a", "rep_b"}, "release-1.2"},
	}
	for _, c := range cases {
		req, err := repos.ParseBatchRequest(c.kind, c.ids, c.branch)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		// accepted => valid
		in := corerepos.StartBatch{ID: "b", Kind: req.Kind, Branch: req.Branch, Repos: req.Repos}
		if err := in.Validate(); err != nil {
			t.Errorf("accepted request fails the core check: %v", err)
		}
		// round trip
		k, ids, b := req.Format()
		again, err := repos.ParseBatchRequest(k, ids, b)
		if err != nil || !reflect.DeepEqual(again, req) {
			t.Errorf("round trip: %+v, %v", again, err)
		}
	}
}

func TestParseBatchRequestRejects(t *testing.T) {
	many := make([]string, 501)
	for i := range many {
		many[i] = fmt.Sprintf("rep_%d", i)
	}
	cases := map[string]struct {
		kind   string
		ids    []string
		branch string
	}{
		"unknown kind":              {"force-push", []string{"rep_a"}, ""},
		"empty kind":                {"", []string{"rep_a"}, ""},
		"reset is not an action":    {"reset", []string{"rep_a"}, ""},
		"checkout without branch":   {"checkout", []string{"rep_a"}, ""},
		"switch without branch":     {"switch", []string{"rep_a"}, ""},
		"fetch with branch":         {"fetch", []string{"rep_a"}, "main"},
		"pull with branch":          {"pull", []string{"rep_a"}, "main"},
		"option-like branch":        {"checkout", []string{"rep_a"}, "--orphan"},
		"branch with space":         {"switch", []string{"rep_a"}, "a b"},
		"branch with ..":            {"switch", []string{"rep_a"}, "a..b"},
		"branch with NUL":           {"switch", []string{"rep_a"}, "a\x00b"},
		"branch ending .lock":       {"switch", []string{"rep_a"}, "x.lock"},
		"no repos":                  {"fetch", nil, ""},
		"too many repos":            {"fetch", many, ""},
		"checkout with two repos":   {"checkout", []string{"rep_a", "rep_b"}, "main"},
		"duplicate repos":           {"fetch", []string{"rep_a", "rep_a"}, ""},
		"malformed repo ID":         {"fetch", []string{"not an id!"}, ""},
		"empty repo ID":             {"fetch", []string{""}, ""},
		"repo ID with path element": {"fetch", []string{"../x"}, ""},
	}
	for name, c := range cases {
		_, err := repos.ParseBatchRequest(c.kind, c.ids, c.branch)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if cl, ok := apperr.ClassOf(err); !ok || cl != apperr.Invalid {
			t.Errorf("%s: class = %v, %v", name, cl, ok)
		}
	}
	// exactly 500 is fine
	if _, err := repos.ParseBatchRequest("fetch", many[:500], ""); err != nil {
		t.Errorf("500 repos: %v", err)
	}
}

func FuzzParseBatchRequest(f *testing.F) {
	f.Add("fetch", "rep_a,rep_b", "")
	f.Add("switch", "rep_a", "feature/x")
	f.Add("checkout", "", "--x")
	f.Fuzz(func(t *testing.T, kind, ids, branch string) {
		req, err := repos.ParseBatchRequest(kind, strings.Split(ids, ","), branch)
		if err != nil {
			return
		}
		in := corerepos.StartBatch{ID: "b", Kind: req.Kind, Branch: req.Branch, Repos: req.Repos}
		if err := in.Validate(); err != nil {
			t.Fatalf("accepted but invalid: %v", err)
		}
		if strings.HasPrefix(req.Branch, "-") || strings.ContainsRune(req.Branch, 0) {
			t.Fatalf("accepted branch %q", req.Branch)
		}
		for _, id := range req.Repos {
			if id.Validate() != nil {
				t.Fatalf("accepted repo %q", id)
			}
		}
		k, i2, b := req.Format()
		again, err := repos.ParseBatchRequest(k, i2, b)
		if err != nil || !reflect.DeepEqual(again, req) {
			t.Fatalf("round trip: %+v, %v", again, err)
		}
	})
}

func TestParseOpenAccepts(t *testing.T) {
	for _, c := range []struct{ editor, file, want string }{
		{"", "", ""}, {"vscode", "", ""}, {"cursor", "src/main.go", "src/main.go"},
		{"idea", `src\main.go`, "src/main.go"}, {"vscode", "./a/b.txt", "a/b.txt"},
	} {
		got, err := repos.ParseOpen(c.editor, c.file)
		if err != nil || got.File.String() != c.want || string(got.Editor) != c.editor {
			t.Errorf("ParseOpen(%q, %q) = %+v, %v", c.editor, c.file, got, err)
		}
	}
}

func TestParseOpenRejects(t *testing.T) {
	cases := map[string][2]string{
		"unknown editor":   {"notepad", ""},
		"upper-case":       {"VSCode", ""},
		"absolute":         {"", "/etc/passwd"},
		"drive letter":     {"", `C:\Windows\x`},
		"escapes the repo": {"", "../secret"},
		"nested escape":    {"", "a/../../b"},
		"NUL":              {"", "a\x00b"},
		"control char":     {"", "a\nb"},
		"option-like":      {"", "-rf"},
	}
	for name, c := range cases {
		if _, err := repos.ParseOpen(c[0], c[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func FuzzParseOpen(f *testing.F) {
	f.Add("vscode", "a/b.go")
	f.Add("", "../x")
	f.Fuzz(func(t *testing.T, editor, file string) {
		req, err := repos.ParseOpen(editor, file)
		if err != nil {
			return
		}
		p := req.File.String()
		if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") || strings.ContainsRune(p, 0) ||
			strings.Contains(p, "..") && (p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../")) {
			t.Fatalf("accepted file %q", p)
		}
		again, err := repos.ParseOpen(string(req.Editor), p)
		if err != nil || again != req {
			t.Fatalf("round trip: %+v, %v", again, err)
		}
	})
}
