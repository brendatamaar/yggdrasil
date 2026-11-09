package worktreeenv

import (
	"errors"
	"strings"
	"testing"
)

func TestScanComposeFileRefusesIndirection(t *testing.T) {
	refused := map[string]string{
		"top-level include":   "include:\n  - other.yml\nservices: {}\n",
		"include short form":  "include: [other.yml]\n",
		"extends":             "services:\n  web:\n    extends:\n      file: base.yml\n      service: web\n",
		"env_file":            "services:\n  web:\n    image: x\n    env_file: .env.prod\n",
		"env_file list":       "services:\n  web:\n    env_file:\n      - a.env\n",
		"quoted key":          "services:\n  web:\n    \"env_file\": a.env\n",
		"single quoted":       "'include':\n  - a.yml\n",
		"flow style":          "services: {web: {image: x, extends: {file: b.yml}}}\n",
		"list item map":       "services:\n  web:\n    - env_file: a\n",
		"CRLF":                "services:\r\n  web:\r\n    env_file: a\r\n",
		"spaces before colon": "include   : a.yml\n",
		"tab indent":          "services:\n\tweb:\n\t\textends: x\n",
		"key at end":          "services:\n  web:\n    env_file:",
	}
	for name, in := range refused {
		err := ScanComposeFile([]byte(in))
		if !errors.Is(err, ErrComposeIndirect) {
			t.Errorf("%s: got %v, want ErrComposeIndirect", name, err)
		}
	}
	accepted := map[string]string{
		"empty":                 "",
		"plain":                 "services:\n  db:\n    image: postgres:16\n    ports:\n      - \"${YGG_PORT_0}:5432\"\n",
		"comment only":          "# include: other.yml\nservices: {}\n",
		"inline comment":        "services:\n  db:\n    image: x # env_file: a\n",
		"word in a value":       "services:\n  db:\n    command: [\"--include\", \"a\"]\n    image: includes:1\n",
		"similar key":           "services:\n  db:\n    my-include: 1\n    env_files: x\n    extends_from: y\n",
		"colon not followed":    "services:\n  db:\n    image: a/include:latest\n",
		"environment variables": "services:\n  db:\n    environment:\n      - EXTENDS=1\n",
	}
	for name, in := range accepted {
		if err := ScanComposeFile([]byte(in)); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

func TestComposeHashCoversBothParts(t *testing.T) {
	a := ComposeHash([]byte("services: {}"), nil)
	if a != ComposeHash([]byte("services: {}"), []byte{}) {
		t.Error("an absent .env must hash like an empty one")
	}
	if a == ComposeHash([]byte("services: {}"), []byte("SECRET=1")) {
		t.Error("a .env change must change the hash")
	}
	if a == ComposeHash([]byte("services: {} "), nil) {
		t.Error("a compose change must change the hash")
	}
	// bytes cannot move from one part to the other
	if ComposeHash([]byte("ab"), []byte("c")) == ComposeHash([]byte("a"), []byte("bc")) {
		t.Error("parts are not delimited")
	}
}

func TestEnvFilePathAndDir(t *testing.T) {
	for file, want := range map[RelPath][2]RelPath{
		"compose.yml":   {"", ".env"},
		"a/compose.yml": {"a", "a/.env"},
		"a/b/c.yaml":    {"a/b", "a/b/.env"},
	} {
		if ComposeDir(file) != want[0] || EnvFilePath(file) != want[1] {
			t.Errorf("%s: %q %q", file, ComposeDir(file), EnvFilePath(file))
		}
	}
}

func FuzzScanComposeFile(f *testing.F) {
	for _, s := range []string{"", "include: a", "services:\n  a:\n    env_file: x", "# extends: a", "{extends: a}", "\x00\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		err := ScanComposeFile([]byte(s))
		if err != nil && !errors.Is(err, ErrComposeIndirect) {
			t.Fatalf("unexpected error type: %v", err)
		}
		// Appending an env_file key on its own line is always refused.
		if err := ScanComposeFile([]byte(strings.TrimRight(s, "\r") + "\nenv_file: x\n")); err == nil {
			t.Fatalf("accepted %q plus an env_file line", s)
		}
	})
}
