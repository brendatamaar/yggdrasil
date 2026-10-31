package main

import (
	"context"
	"strings"
	"testing"
)

func TestParseServeArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    bool
		wantErr string
	}{
		{name: "default opens the browser"},
		{name: "flag", args: []string{"--no-browser"}, want: true},
		{name: "env 1", env: map[string]string{"YGG_NO_BROWSER": "1"}, want: true},
		{name: "env true", env: map[string]string{"YGG_NO_BROWSER": "TRUE"}, want: true},
		{name: "env 0", env: map[string]string{"YGG_NO_BROWSER": "0"}},
		{name: "env 0 and flag", args: []string{"--no-browser"}, env: map[string]string{"YGG_NO_BROWSER": "0"}, want: true},
		{name: "bad env", env: map[string]string{"YGG_NO_BROWSER": "maybe"}, wantErr: "YGG_NO_BROWSER"},
		{name: "unknown option", args: []string{"--browser"}, wantErr: "--browser"},
		{name: "extra argument", args: []string{"--no-browser", "x"}, wantErr: "\"x\""},
	}
	for _, tt := range tests {
		got, err := parseServeArgs(tt.args, env(tt.env))
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%s: err = %v, want it to mention %q", tt.name, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got.noBrowser != tt.want {
			t.Errorf("%s: noBrowser = %v, %v; want %v", tt.name, got.noBrowser, err, tt.want)
		}
	}
}

func TestRunServeRejectsUnknownOption(t *testing.T) {
	s, _, _ := testStreams("", map[string]string{"YGG_DATA_DIR": t.TempDir()})
	if err := run(context.Background(), []string{"serve", "--bogus"}, s); err == nil || !strings.Contains(err.Error(), "--bogus") {
		t.Errorf("serve --bogus: %v", err)
	}
}

// Bare `ygg` (no arguments) means serve; a loopback-only address error proves
// it got as far as the config without panicking on the missing argument.
func TestRunWithoutArgumentsServes(t *testing.T) {
	s, _, _ := testStreams("", map[string]string{"YGG_ADDR": "0.0.0.0:4747"})
	if err := run(context.Background(), nil, s); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("bare ygg: %v", err)
	}
	if err := run(context.Background(), []string{"--no-browser"}, s); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("ygg --no-browser: %v", err)
	}
}

func TestLaunchURL(t *testing.T) {
	if got := launchURL("127.0.0.1:4747", "abc"); got != "http://127.0.0.1:4747/?k=abc" {
		t.Errorf("with token: %q", got)
	}
	if got := launchURL("127.0.0.1:4747", ""); got != "http://127.0.0.1:4747/" {
		t.Errorf("without token: %q", got)
	}
}
