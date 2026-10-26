// Package safetytest holds the fakes and contract suites of the capabilities
// that internal/safety consumers use: the config scanner and the snapshotter.
package safetytest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// FakeScanner is an in-memory safety.ConfigScanner. Files maps a repo-relative
// slash path to its content. It uses the same selection and hashing as the
// real scanner, and fails the way the real one does for oversized files.
type FakeScanner struct {
	Files map[string]string
}

// Scan implements safety.ConfigScanner; dir is ignored.
func (f FakeScanner) Scan(ctx context.Context, _ string, paths, names []string) (guard.Scan, error) {
	if err := ctx.Err(); err != nil {
		return guard.Scan{}, err
	}
	keys := make([]string, 0, len(f.Files))
	for k := range f.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var in []safety.ScanInput
	for _, k := range keys {
		if !safety.SelectConfig(k, paths, names) {
			continue
		}
		if len(f.Files[k]) > safety.MaxConfigFileSize {
			return guard.Scan{}, fmt.Errorf("%w: %s is larger than 1 MiB", safety.ErrScan, k)
		}
		in = append(in, safety.ScanInput{Path: k, Content: []byte(f.Files[k])})
	}
	return safety.BuildScan(in), nil
}

// ScannerFactory returns a scanner and the repo directory holding files (paths
// relative, slash separated).
type ScannerFactory func(t *testing.T, files map[string]string) (safety.ConfigScanner, string)

// FakeScannerFactory is the factory for FakeScanner.
func FakeScannerFactory(_ *testing.T, files map[string]string) (safety.ConfigScanner, string) {
	return FakeScanner{Files: files}, "repo"
}

// ScannerContract is run against the fake and the real scanner.
func ScannerContract(t *testing.T, mk ScannerFactory) {
	paths := []string{".claude/settings.json", ".mcp.json", ".claude/agents"}
	names := []string{"CLAUDE.md"}
	scan := func(t *testing.T, files map[string]string) (guard.Scan, error) {
		t.Helper()
		s, dir := mk(t, files)
		return s.Scan(context.Background(), dir, paths, names)
	}
	must := func(t *testing.T, files map[string]string) guard.Scan {
		t.Helper()
		sc, err := scan(t, files)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		return sc
	}
	base := func() map[string]string {
		return map[string]string{
			".claude/settings.json": `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"node hook.js"}]}]}}`,
			".claude/agents/rev.md": "review",
			"CLAUDE.md":             "rules",
			"src/main.go":           "package main",
			"unrelated/readme.txt":  "x",
		}
	}

	t.Run("same files same hash", func(t *testing.T) {
		a, b := must(t, base()), must(t, base())
		if a.Hash == "" || a.Hash != b.Hash {
			t.Fatalf("hash %q vs %q", a.Hash, b.Hash)
		}
		if len(a.Hash) != 64 {
			t.Fatalf("hash is not 64 hex digits: %q", a.Hash)
		}
	})
	t.Run("unrelated files do not matter", func(t *testing.T) {
		f := base()
		f["src/main.go"] = "package other"
		if must(t, f).Hash != must(t, base()).Hash {
			t.Fatal("an unrelated file changed the hash")
		}
	})
	t.Run("content change changes the hash", func(t *testing.T) {
		f := base()
		f[".claude/settings.json"] += " "
		if must(t, f).Hash == must(t, base()).Hash {
			t.Fatal("hash did not change")
		}
	})
	t.Run("rename changes the hash", func(t *testing.T) {
		f := base()
		f[".claude/agents/other.md"] = f[".claude/agents/rev.md"]
		delete(f, ".claude/agents/rev.md")
		if must(t, f).Hash == must(t, base()).Hash {
			t.Fatal("hash did not change")
		}
	})
	t.Run("added file changes the hash", func(t *testing.T) {
		f := base()
		f[".mcp.json"] = `{"mcpServers":{"x":{"command":"npx","args":["srv"]}}}`
		if must(t, f).Hash == must(t, base()).Hash {
			t.Fatal("hash did not change")
		}
	})
	t.Run("empty set is Empty", func(t *testing.T) {
		sc := must(t, map[string]string{"src/main.go": "x"})
		if !sc.Empty() || sc.Hash != "" {
			t.Fatalf("scan = %+v", sc)
		}
	})
	t.Run("name found at depth 3", func(t *testing.T) {
		sc := must(t, map[string]string{"a/b/c/CLAUDE.md": "deep"})
		if len(sc.Files) != 1 || sc.Files[0].Path != "a/b/c/CLAUDE.md" {
			t.Fatalf("scan = %+v", sc)
		}
	})
	t.Run("oversized file is an error naming it", func(t *testing.T) {
		f := base()
		f["sub/CLAUDE.md"] = strings.Repeat("x", safety.MaxConfigFileSize+1)
		_, err := scan(t, f)
		if err == nil || !errors.Is(err, safety.ErrScan) || !strings.Contains(err.Error(), "sub/CLAUDE.md") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), `\`) || strings.Contains(err.Error(), "Temp") {
			t.Fatalf("error leaks a path: %v", err)
		}
	})
	t.Run("hooks and servers are summarised", func(t *testing.T) {
		f := base()
		f[".mcp.json"] = `{"mcpServers":{"db":{"command":"npx","args":["-y","srv"]}}}`
		sc := must(t, f)
		var all []string
		for _, sf := range sc.Files {
			all = append(all, sf.WouldRun...)
		}
		j := strings.Join(all, "\n")
		if !strings.Contains(j, "hook: node hook.js") || !strings.Contains(j, "MCP server db: npx -y srv") {
			t.Fatalf("WouldRun = %q", j)
		}
	})
	t.Run("non-UTF-8 is hashed and opaque", func(t *testing.T) {
		f := base()
		g := base()
		f["CLAUDE.md"] = "\xff\xfe"
		g["CLAUDE.md"] = "\xff\xfd"
		a, b := must(t, f), must(t, g)
		if a.Hash == b.Hash {
			t.Fatal("binary content not hashed")
		}
		for _, sf := range a.Files {
			if sf.Path == "CLAUDE.md" && !sf.Opaque {
				t.Fatal("not marked opaque")
			}
		}
	})
}
