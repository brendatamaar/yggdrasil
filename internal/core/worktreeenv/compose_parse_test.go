package worktreeenv

import (
	"errors"
	"strings"
	"testing"
)

func TestParseComposeFile(t *testing.T) {
	for in, want := range map[string]RelPath{
		"docker-compose.yml": "docker-compose.yml", `deploy\compose.YAML`: "deploy/compose.YAML", "./a/b.yaml": "a/b.yaml",
	} {
		got, err := ParseComposeFile(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
		// round trip and accepted => valid
		if again, err := ParseComposeFile(string(got)); err != nil || again != got {
			t.Errorf("round trip %q: %v %v", got, again, err)
		}
		if got.Validate() != nil {
			t.Errorf("%q accepted but not valid", got)
		}
	}
}

func TestParseComposeFileRejects(t *testing.T) {
	cases := map[string]error{
		"":                                ErrPathEmpty,
		"/etc/c.yml":                      ErrPathAbsolute,
		`C:\c.yml`:                        ErrPathAbsolute,
		"../c.yml":                        ErrPathParent,
		".git/c.yml":                      ErrPathGit,
		"a\x00.yml":                       ErrPathControl,
		"*.yml":                           ErrPathGlob,
		"nul.yml":                         ErrPathReserved,
		strings.Repeat("a", 261) + ".yml": ErrPathLong,
		"compose.json":                    ErrComposeExt,
		"compose":                         ErrComposeExt,
		"compose.yml.txt":                 ErrComposeExt,
	}
	for in, want := range cases {
		if _, err := ParseComposeFile(in); !errors.Is(err, want) {
			t.Errorf("%q: got %v want %v", in, err, want)
		}
	}
}

func TestParseProjectName(t *testing.T) {
	for _, n := range []string{"a", "feat-1", "0-9", strings.Repeat("a", 40)} {
		p, err := ParseProjectName(n)
		if err != nil || string(p) != "ygg-"+n || p.Name() != n || p.Validate() != nil {
			t.Errorf("%q: %v %v", n, p, err)
		}
		if again, err := ParseProjectName(p.Name()); err != nil || again != p {
			t.Errorf("round trip %q", n)
		}
	}
	cases := map[string]error{
		"":                      ErrProjectEmpty,
		"Feat":                  ErrProjectChars,
		"a_b":                   ErrProjectChars,
		"a b":                   ErrProjectChars,
		"a/b":                   ErrProjectChars,
		"é":                     ErrProjectChars,
		strings.Repeat("a", 41): ErrProjectLong,
	}
	for in, want := range cases {
		if _, err := ParseProjectName(in); !errors.Is(err, want) {
			t.Errorf("%q: got %v want %v", in, err, want)
		}
	}
	if ProjectName("feat").Validate() == nil {
		t.Error("a name without the ygg- prefix is not valid")
	}
}

func TestParsePs(t *testing.T) {
	for _, r := range []PsResult{PsRunning, PsNotRunning} {
		got, err := ParsePs(FormatPs(r))
		if err != nil || got != r {
			t.Errorf("round trip %s: %v %v", r, got, err)
		}
	}
	ok := map[string]PsResult{
		`[{"Name":"a","State":"running"},{"State":"exited"}]`: PsRunning,
		`[{"State":"exited"}]`:                                PsNotRunning,
		"[]":                                                  PsNotRunning,
		"{\"State\":\"exited\"}\n{\"State\":\"restarting\"}\r\n": PsRunning,
		"  \n": PsNotRunning,
	}
	for in, want := range ok {
		got, err := ParsePs([]byte(in))
		if err != nil || got != want || got.Validate() != nil {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"not json", "[{", `{"State":`, "{}\nfoo", "[1,2"} {
		if _, err := ParsePs([]byte(in)); !errors.Is(err, ErrPsUndecodable) {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func FuzzParsePs(f *testing.F) {
	f.Add([]byte(`[{"State":"running"}]`))
	f.Add([]byte("{}\n{}"))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := ParsePs(b)
		if err == nil && r.Validate() != nil {
			t.Fatalf("accepted but invalid: %q", r)
		}
	})
}

func FuzzParseComposeFile(f *testing.F) {
	f.Add("a/b.yml")
	f.Add("../x.yaml")
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParseComposeFile(s)
		if err != nil {
			return
		}
		if p.Validate() != nil {
			t.Fatalf("accepted but invalid: %q", p)
		}
		if again, err := ParseComposeFile(string(p)); err != nil || again != p {
			t.Fatalf("round trip %q -> %q, %v", p, again, err)
		}
	})
}

func FuzzParseProjectName(f *testing.F) {
	f.Add("feat-1")
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParseProjectName(s)
		if err != nil {
			return
		}
		if p.Validate() != nil || p.Name() != s {
			t.Fatalf("accepted %q but %q", s, p)
		}
	})
}
