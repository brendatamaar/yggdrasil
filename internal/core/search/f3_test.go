package search

import "testing"

func TestEditorURLStripsVerbatimPrefixes(t *testing.T) {
	cases := []struct {
		e    Editor
		path string
		want string
	}{
		{EditorVSCode, `\\?\C:\x`, "vscode://file/C:/x:3:4"},
		{EditorCursor, `\\?\C:\acme shop\a.go`, "cursor://file/C:/acme%20shop/a.go:3:4"},
		// TODO(backlog item 5, docs/phases/phase-2.md): the UNC host is lost in
		// the two UNC cases; once production returns no link for UNC, expect "".
		{EditorVSCode, `\\?\UNC\srv\share\a.go`, "vscode://file/srv/share/a.go:3:4"},
		{EditorIdea, `\\?\C:\x`, "idea://open?file=C%3A/x&line=3"},
		{EditorIdea, `\\?\UNC\srv\share\a.go`, "idea://open?file=//srv/share/a.go&line=3"},
	}
	for _, c := range cases {
		got, err := EditorURL(c.e, c.path, 3, 4)
		if err != nil || got != c.want {
			t.Errorf("%v %q: got %q (%v), want %q", c.e, c.path, got, err, c.want)
		}
	}
}

func TestStripVerbatim(t *testing.T) {
	for in, want := range map[string]string{
		`\\?\C:\x`:            `C:\x`,
		`\\?\UNC\srv\share\a`: `\\srv\share\a`,
		`C:\x`:                `C:\x`,
		`/home/dev/a`:         `/home/dev/a`,
		`\\srv\share\a`:       `\\srv\share\a`,
	} {
		if got := stripVerbatim(in); got != want {
			t.Errorf("stripVerbatim(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStepUnknownFailureKindBecomesGitFailed(t *testing.T) {
	s, _ := Step(Search{}, Begin{Query: Query{Pattern: "x", Mode: ModeLiteral}, Repos: []RepoRef{{ID: "a", Name: "a"}}})
	s, _ = Step(s, RepoFailed{Repo: "a", Kind: "kaboom"})
	if !s.Done() || s.Problems["a"] != ProblemGitFailed {
		t.Fatalf("state = %+v, want done with git_failed", s)
	}
}
