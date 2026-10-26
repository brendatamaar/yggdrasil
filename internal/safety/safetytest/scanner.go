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
		if len(in) >= safety.MaxConfigFiles {
			return guard.Scan{}, fmt.Errorf("%w: more than %d configuration files", safety.ErrScan, safety.MaxConfigFiles)
		}
		in = append(in, safety.ScanInput{Path: k, Content: []byte(f.Files[k])})
	}
	return safety.BuildScan(in), nil
}

// FakeEnvScanner is FakeScanner plus links and unreadable files, with the
// failure rules of the real scanner.
type FakeEnvScanner struct {
	Env ScannerEnv
}

// Scan implements safety.ConfigScanner; dir is ignored.
func (f FakeEnvScanner) Scan(ctx context.Context, dir string, paths, names []string) (guard.Scan, error) {
	for _, l := range f.Env.Links {
		if safety.LinkBlocksConfig(l, paths, names) {
			return guard.Scan{}, fmt.Errorf("%w: %s is a link", safety.ErrScan, l)
		}
	}
	for _, u := range f.Env.Unreadable {
		if safety.LinkBlocksConfig(u, paths, names) {
			return guard.Scan{}, fmt.Errorf("%w: %s can't be read", safety.ErrScan, u)
		}
	}
	return FakeScanner{Files: f.Env.Files}.Scan(ctx, dir, paths, names)
}

// ScannerEnv describes a repo for the link contract: Files are plain files,
// Links are folders (repo-relative) that are links or junctions to a folder
// holding agent settings, Unreadable are files that can't be opened.
type ScannerEnv struct {
	Files      map[string]string
	Links      []string
	Unreadable []string
}

// EnvFactory builds a scanner and its repo directory for env.
type EnvFactory func(t *testing.T, env ScannerEnv) (safety.ConfigScanner, string)

// FakeLinkScannerFactory is the EnvFactory of FakeEnvScanner.
func FakeLinkScannerFactory(_ *testing.T, env ScannerEnv) (safety.ConfigScanner, string) {
	return FakeEnvScanner{Env: env}, "repo"
}

// LinkScannerContract checks the fail-closed rules for links, junctions and
// unreadable files. It is run against the fake and the real scanner.
func LinkScannerContract(t *testing.T, mk EnvFactory) {
	paths := []string{".claude/settings.json", ".mcp.json", ".claude/agents", ".claude/skills"}
	names := []string{"CLAUDE.md"}
	scan := func(env ScannerEnv) error {
		t.Helper()
		s, dir := mk(t, env)
		_, err := s.Scan(context.Background(), dir, paths, names)
		return err
	}
	refused := func(name string, env ScannerEnv, want string) {
		t.Run(name, func(t *testing.T) {
			err := scan(env)
			if err == nil || !errors.Is(err, safety.ErrScan) || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want ErrScan naming %q", err, want)
			}
			if strings.Contains(err.Error(), `\`) || strings.Contains(err.Error(), "Temp") {
				t.Fatalf("error leaks a path: %v", err)
			}
		})
	}
	other := map[string]string{"src/main.go": "x"}
	refused("linked .claude folder", ScannerEnv{Files: other, Links: []string{".claude"}}, ".claude")
	refused("linked .claude folder, other case", ScannerEnv{Files: other, Links: []string{".CLAUDE"}}, ".CLAUDE")
	refused("linked .claude/agents", ScannerEnv{Files: map[string]string{".claude/settings.json": "{}"}, Links: []string{".claude/agents"}}, ".claude/agents")
	refused("linked skills folder", ScannerEnv{Files: map[string]string{".claude/x.md": "x"}, Links: []string{".claude/skills"}}, ".claude/skills")
	refused("linked folder named like a config name", ScannerEnv{Files: other, Links: []string{"sub/CLAUDE.md"}}, "sub/CLAUDE.md")
	refused("unreadable config file", ScannerEnv{Files: map[string]string{".mcp.json": "{}", "CLAUDE.md": "x"}, Unreadable: []string{".mcp.json"}}, ".mcp.json")
	t.Run("unrelated link is fine", func(t *testing.T) {
		if err := scan(ScannerEnv{Files: other, Links: []string{"vendor-link"}}); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unreadable unrelated file is fine", func(t *testing.T) {
		if err := scan(ScannerEnv{Files: map[string]string{"src/a.go": "x"}, Unreadable: []string{"src/a.go"}}); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
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
	t.Run("nothing inside .git is read", func(t *testing.T) {
		f := base()
		f[".git/CLAUDE.md"] = "x"
		f[".git/hooks/pre-commit"] = "x"
		f[".GIT/CLAUDE.md"] = "x"
		if must(t, f).Hash != must(t, base()).Hash {
			t.Fatal(".git content changed the hash")
		}
	})
	t.Run("names and paths fold case", func(t *testing.T) {
		sc := must(t, map[string]string{"sub/claude.MD": "y", ".CLAUDE/Agents/x.md": "z"})
		if len(sc.Files) != 2 {
			t.Fatalf("scan = %+v", sc)
		}
	})
	t.Run("too many matching files is an error", func(t *testing.T) {
		old := safety.MaxConfigFiles
		safety.MaxConfigFiles = 20
		t.Cleanup(func() { safety.MaxConfigFiles = old })
		f := base()
		for i := 0; i <= safety.MaxConfigFiles; i++ {
			f[fmt.Sprintf(".claude/agents/a%05d.md", i)] = "x"
		}
		if _, err := scan(t, f); err == nil || !errors.Is(err, safety.ErrScan) {
			t.Fatalf("err = %v", err)
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
