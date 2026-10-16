package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func ok(detail string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return detail, nil }
}

func bad(msg string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return "", advice(msg) }
}

func TestDoctorOutputWhenEverythingRequiredIsThere(t *testing.T) {
	var out bytes.Buffer
	code := runDoctor(context.Background(), &out, []probe{
		{name: "git", required: true, check: ok("git version 2.45.1")},
		{name: "data folder", required: true, check: ok(`C:\data\ygg`)},
		{name: "Node.js", check: bad("not found. Only needed for Node repos. Get it from https://nodejs.org.")},
		{name: "Claude Code", check: ok("installed and starts")},
	})
	want := `Yggdrasil doctor

  ok    git            git version 2.45.1
  ok    data folder    C:\data\ygg
  --    Node.js        not found. Only needed for Node repos. Get it from https://nodejs.org.
  ok    Claude Code    installed and starts

Everything required is in place. 1 optional tool(s) were not found; install one only if you need it.
`
	if code != 0 || out.String() != want {
		t.Errorf("exit %d, output:\n%s\nwant:\n%s", code, out.String(), want)
	}
}

func TestDoctorFailsOnlyForRequiredProbes(t *testing.T) {
	var out bytes.Buffer
	code := runDoctor(context.Background(), &out, []probe{
		{name: "git", required: true, check: bad("not found. Install Git for Windows from https://git-scm.com.")},
		{name: "data folder", required: true, check: bad("C:\\ro can't be written to. Set YGG_DATA_DIR to a folder you can write to.")},
		{name: "Go", check: bad("not found.")},
		{name: "uv", check: ok("uv 0.5.0")},
	})
	text := out.String()
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, want := range []string{
		"  FAIL  git            not found. Install Git for Windows",
		"  FAIL  data folder    C:\\ro can't be written to.",
		"  --    Go             not found.",
		"2 required check(s) failed: git, data folder. Fix them, then run `ygg doctor` again.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	// An optional tool that is missing never makes the doctor fail.
	if strings.Contains(text, "FAIL  Go") {
		t.Errorf("an optional tool was reported as a failure:\n%s", text)
	}
}

func TestDoctorAllGood(t *testing.T) {
	var out bytes.Buffer
	if code := runDoctor(context.Background(), &out, []probe{{name: "git", required: true, check: ok("v")}}); code != 0 || !strings.HasSuffix(out.String(), "Everything is in place.\n") {
		t.Errorf("exit %d:\n%s", code, out.String())
	}
}

func fakeToolEnvFor(t *testing.T, mode string) []string {
	t.Helper()
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{fakeToolEnv: mode}})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestToolProbeReadsTheFirstLineOfTheVersion(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tl := tool{name: "faketool", bin: exe, args: []string{"--version"}, why: "Only for tests."}

	detail, err := toolProbe(tl, fakeToolEnvFor(t, "version")).check(context.Background())
	if err != nil || detail != "faketool 1.2.3" {
		t.Errorf("version = %q, %v", detail, err)
	}
	// Some tools (java) print their version on stderr.
	detail, err = toolProbe(tl, fakeToolEnvFor(t, "stderr")).check(context.Background())
	if err != nil || detail != "openjdk 21.0.2" {
		t.Errorf("stderr version = %q, %v", detail, err)
	}
	_, err = toolProbe(tl, fakeToolEnvFor(t, "fail")).check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "didn't run") || !strings.Contains(err.Error(), "Only for tests.") {
		t.Errorf("failing tool = %v", err)
	}
	missing := tool{name: "ghost", bin: "no-such-tool-xyz", args: []string{"--version"}, why: "Install it."}
	_, err = toolProbe(missing, fakeToolEnvFor(t, "version")).check(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "not found.") || !strings.Contains(err.Error(), "Install it.") {
		t.Errorf("missing tool = %v", err)
	}
	var a advice
	if !errors.As(err, &a) {
		t.Errorf("probe errors are advice, got %T", err)
	}
}

func TestPythonTwoIsNotAccepted(t *testing.T) {
	for _, ok := range []string{"Python 3.12.1", "Python 3.9.0"} {
		if err := python3Only(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"Python 2.7.18", "Python 4.0", "python", ""} {
		err := python3Only(bad)
		if err == nil || !strings.Contains(err.Error(), "Python 3") {
			t.Errorf("%q accepted or unhelpful: %v", bad, err)
		}
	}

	// Through the probe: a fake tool printing a Python 2 version is reported.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tl := tool{name: "Python", bin: exe, args: []string{"--version"}, accept: python3Only, why: "Only for tests."}
	_, err = toolProbe(tl, fakeToolEnvFor(t, "version")).check(context.Background()) // prints "faketool 1.2.3"
	if err == nil || !strings.Contains(err.Error(), "faketool 1.2.3") || !strings.Contains(err.Error(), "Python 3") {
		t.Errorf("a version the tool's accept rejects: %v", err)
	}
	// And the real list uses it for Python only.
	for _, tl := range tools {
		if (tl.name == "Python") != (tl.accept != nil) {
			t.Errorf("%s: accept = %v", tl.name, tl.accept != nil)
		}
	}
}

func TestDataDirProbe(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "ygg")
	detail, err := dataDirProbe(dir).check(context.Background())
	if err != nil || detail != dir {
		t.Fatalf("a new folder: %q, %v", detail, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".doctor-*")); len(left) != 0 {
		t.Errorf("the probe left files behind: %v", left)
	}
	// A file where the folder should be cannot become one.
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dataDirProbe(filepath.Join(file, "ygg")).check(context.Background()); err == nil || !strings.Contains(err.Error(), "YGG_DATA_DIR") {
		t.Errorf("an impossible folder: %v", err)
	}
}

func TestDefaultProbesPutGitAndTheDataFolderFirst(t *testing.T) {
	probes := defaultProbes(config{dataDir: t.TempDir()}, nil)
	if probes[0].name != "git" || !probes[0].required || probes[1].name != "data folder" || !probes[1].required {
		t.Errorf("first probes = %s, %s", probes[0].name, probes[1].name)
	}
	names := map[string]bool{}
	for _, p := range probes {
		if names[p.name] {
			t.Errorf("duplicate probe %q", p.name)
		}
		names[p.name] = true
		if p.name != "git" && p.name != "data folder" && p.required {
			t.Errorf("%s is required: only git and the data folder are", p.name)
		}
	}
	for _, want := range []string{"Claude Code", "Node.js", "Python", "Java", ".NET"} {
		if !names[want] {
			t.Errorf("no probe for %s", want)
		}
	}
}

func TestAgentProbeShowsOnlyPlainLanguage(t *testing.T) {
	_, err := agentProbe("Some Agent", func(context.Context) error {
		return errors.New(`open C:\Users\someone\secret: access denied`)
	}).check(context.Background())
	if err == nil || strings.Contains(err.Error(), "someone") || strings.Contains(err.Error(), "secret") {
		t.Errorf("an internal error was shown: %v", err)
	}
}
