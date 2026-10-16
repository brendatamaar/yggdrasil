package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A test binary doubles as a fake tool for the doctor tests. It never falls
// through to the test suite: with the marker set it prints and exits.
const fakeToolEnv = "YGG_FAKE_TOOL"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeToolEnv); mode != "" {
		switch mode {
		case "version":
			fmt.Println("faketool 1.2.3")
			fmt.Println("a second line that must not show")
			os.Exit(0)
		case "stderr":
			fmt.Fprintln(os.Stderr, "openjdk 21.0.2")
			os.Exit(0)
		case "fail":
			os.Exit(3)
		}
		fmt.Fprintln(os.Stderr, "unknown fake tool mode", mode)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestListenAddr(t *testing.T) {
	tests := []struct {
		env     string
		want    string
		wantErr bool
	}{
		{env: "", want: defaultAddr},
		{env: "127.0.0.1:5000", want: "127.0.0.1:5000"},
		{env: "[::1]:5000", want: "[::1]:5000"},
		{env: "0.0.0.0:4747", wantErr: true},
		{env: ":4747", wantErr: true},
		{env: "localhost:4747", wantErr: true},
		{env: "192.168.1.10:4747", wantErr: true},
		{env: "4747", wantErr: true},
	}
	for _, tt := range tests {
		got, err := listenAddr(tt.env)
		if tt.wantErr {
			if err == nil {
				t.Errorf("listenAddr(%q) = %q, want error", tt.env, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("listenAddr(%q) = %q, %v; want %q", tt.env, got, err, tt.want)
		}
	}
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig(t *testing.T) {
	home := func() (string, error) { return `C:\Users\someone\AppData\Roaming`, nil }
	cfg, err := loadConfig(env(nil), home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != defaultAddr || cfg.dataDir != filepath.Join(`C:\Users\someone\AppData\Roaming`, "ygg") || cfg.mockDelay != 250*time.Millisecond {
		t.Errorf("defaults = %+v", cfg)
	}

	abs := t.TempDir()
	cfg, err = loadConfig(env(map[string]string{"YGG_ADDR": "127.0.0.1:5555", "YGG_DATA_DIR": abs, "YGG_LOG": "DEBUG", "YGG_MOCK_DELAY_MS": "0"}), home)
	if err != nil || cfg.addr != "127.0.0.1:5555" || cfg.dataDir != abs || cfg.mockDelay != 0 || cfg.level.String() != "DEBUG" {
		t.Errorf("overrides = %+v, %v", cfg, err)
	}

	for name, m := range map[string]map[string]string{
		"remote address":     {"YGG_ADDR": "0.0.0.0:4747"},
		"relative data dir":  {"YGG_DATA_DIR": "data"},
		"unknown log level":  {"YGG_LOG": "loud"},
		"delay not a number": {"YGG_MOCK_DELAY_MS": "slow"},
		"delay too long":     {"YGG_MOCK_DELAY_MS": "600000"},
		"negative delay":     {"YGG_MOCK_DELAY_MS": "-1"},
	} {
		if _, err := loadConfig(env(m), home); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := loadConfig(env(nil), func() (string, error) { return "", errors.New("no home") }); err == nil || !strings.Contains(err.Error(), "YGG_DATA_DIR") {
		t.Errorf("no home folder: %v", err)
	}
}

func testStreams(in string, m map[string]string) (streams, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return streams{
		in: strings.NewReader(in), out: &out, err: &errOut, getenv: env(m),
		userConfigDir: func() (string, error) { return os.TempDir(), nil },
	}, &out, &errOut
}

func TestRunHelpVersionAndUnknownCommand(t *testing.T) {
	s, out, _ := testStreams("", nil)
	if err := run(context.Background(), []string{"help"}, s); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, want := range []string{"serve", "doctor", "seed", "YGG_ADDR", "YGG_DATA_DIR"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	// The hook is an internal command and stays out of the help.
	if strings.Contains(help, "hook") {
		t.Errorf("help mentions the hidden hook command:\n%s", help)
	}

	s, out, _ = testStreams("", nil)
	if err := run(context.Background(), []string{"version"}, s); err != nil || out.String() != "ygg dev\n" {
		t.Errorf("version = %q, %v", out.String(), err)
	}

	s, _, errOut := testStreams("", nil)
	err := run(context.Background(), []string{"frobnicate"}, s)
	if err == nil || !strings.Contains(err.Error(), "frobnicate") || !strings.Contains(errOut.String(), "Usage:") {
		t.Errorf("unknown command: %v / %q", err, errOut.String())
	}
}

// The hook fails closed: without its configuration a tool call is blocked,
// and the exit code reaches the CLI.
func TestHookCommandExitCodes(t *testing.T) {
	s, out, errOut := testStreams(`{"tool_name":"Read"}`, nil)
	err := run(context.Background(), []string{"hook", "--event", "PreToolUse"}, s)
	var ee exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("PreToolUse without configuration: %v, want exit 2", err)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "blocked") {
		t.Errorf("hook output: stdout %q, stderr %q", out.String(), errOut.String())
	}

	s, _, _ = testStreams(`{}`, nil)
	if err := run(context.Background(), []string{"hook", "--event", "PostToolUse"}, s); !errors.As(err, &ee) || ee.code != 1 {
		t.Errorf("PostToolUse without configuration: %v, want exit 1", err)
	}
}

func TestServeRefusesARemoteAddress(t *testing.T) {
	s, _, _ := testStreams("", map[string]string{"YGG_ADDR": "0.0.0.0:4747"})
	if err := run(context.Background(), []string{"serve"}, s); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("serve on 0.0.0.0: %v", err)
	}
}
