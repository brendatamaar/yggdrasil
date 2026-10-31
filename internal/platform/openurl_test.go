package platform

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckLoopbackURL(t *testing.T) {
	ok := []string{
		"http://127.0.0.1:4747/",
		"http://127.0.0.1:4747",
		"http://127.0.0.1:1/?k=abc123",
		"http://127.0.0.1:65535/a/b?k=x_y-z#f",
	}
	for _, u := range ok {
		if err := checkLoopbackURL(u); err != nil {
			t.Errorf("%q rejected: %v", u, err)
		}
	}
	bad := map[string]string{
		"empty":        "",
		"https":        "https://127.0.0.1:4747/",
		"uppercase":    "HTTP://127.0.0.1:4747/",
		"localhost":    "http://localhost:4747/",
		"other host":   "http://example.com:4747/",
		"ipv6":         "http://[::1]:4747/",
		"other ip":     "http://127.0.0.2:4747/",
		"lookalike":    "http://127.0.0.1.evil.example:4747/",
		"no port":      "http://127.0.0.1/",
		"port 0":       "http://127.0.0.1:0/",
		"port big":     "http://127.0.0.1:65536/",
		"port padded":  "http://127.0.0.1:04747/",
		"file":         "file:///C:/Windows/System32/calc.exe",
		"javascript":   "javascript:alert(1)",
		"space":        "http://127.0.0.1:4747/a b",
		"leading sp":   " http://127.0.0.1:4747/",
		"newline":      "http://127.0.0.1:4747/\nx",
		"cr":           "http://127.0.0.1:4747/\r",
		"tab":          "http://127.0.0.1:4747/\t",
		"nul":          "http://127.0.0.1:4747/\x00",
		"userinfo":     "http://user@127.0.0.1:4747/",
		"userinfo pw":  "http://user:pw@127.0.0.1:4747/",
		"host in user": "http://127.0.0.1:4747@evil.example/",
		"backslash":    `http://127.0.0.1:4747\@evil.example/`,
		"opaque":       "http:127.0.0.1:4747",
		"non-ascii":    "http://127.0.0.1:4747/é",
		"option":       "--help",
	}
	for name, u := range bad {
		err := checkLoopbackURL(u)
		if err == nil {
			t.Errorf("%s: %q accepted", name, u)
			continue
		}
		if u != "" && strings.Contains(err.Error(), u) {
			t.Errorf("%s: error echoes the URL: %v", name, err)
		}
	}
}

func TestOpenURLBuildsCommandWithoutShell(t *testing.T) {
	const link = "http://127.0.0.1:4747/?k=SECRETTOKEN&x=1"
	if _, _, _, err := urlOpener(link); err != nil {
		t.Skipf("no opener on this machine: %v", err)
	}
	old := launchProc
	t.Cleanup(func() { launchProc = old })

	var gotExe string
	var gotArgv, gotEnv []string
	launchProc = func(exe string, argv, env []string) error {
		gotExe, gotArgv, gotEnv = exe, argv, env
		return nil
	}
	if err := OpenURL(link); err != nil {
		t.Fatal(err)
	}
	if len(gotArgv) == 0 || gotArgv[0] != gotExe {
		t.Fatalf("argv %q does not start with exe %q", gotArgv, gotExe)
	}
	if got := gotArgv[len(gotArgv)-1]; got != link {
		t.Errorf("link is not the last argument verbatim: %q", got)
	}
	n := 0
	for _, a := range gotArgv {
		if strings.Contains(a, "SECRETTOKEN") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("token appears in %d arguments, want 1", n)
	}
	base := strings.ToLower(gotExe)
	for _, sh := range []string{"cmd.exe", "powershell", "pwsh", "/sh", "/bash"} {
		if strings.Contains(base, sh) {
			t.Errorf("opener %q is a shell", gotExe)
		}
	}
	for _, a := range gotArgv[:len(gotArgv)-1] {
		if strings.EqualFold(a, "/c") || a == "-c" || strings.EqualFold(a, "start") {
			t.Errorf("shell argument %q in %q", a, gotArgv)
		}
	}
	for _, e := range gotEnv {
		if strings.Contains(e, "SECRETTOKEN") {
			t.Errorf("token in environment")
		}
	}
}

func TestOpenURLErrorsDoNotEchoTheLink(t *testing.T) {
	const link = "http://127.0.0.1:4747/?k=SECRETTOKEN"
	if _, _, _, err := urlOpener(link); err != nil {
		t.Skipf("no opener on this machine: %v", err)
	}
	old := launchProc
	t.Cleanup(func() { launchProc = old })
	launchProc = func(exe string, argv, env []string) error {
		return errors.New("start " + exe + " " + strings.Join(argv[1:], " ") + ": boom k=SECRETTOKEN")
	}
	err := OpenURL(link)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, s := range []string{"SECRETTOKEN", link} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error leaks %q: %v", s, err)
		}
	}
	if err := OpenURL("https://example.com/?k=SECRETTOKEN"); err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Errorf("refusal must not echo the link: %v", err)
	}
}
