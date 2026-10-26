package safety_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/safety/safetytest"
)

func fsFactory(t *testing.T, files map[string]string) (safety.ConfigScanner, string) {
	t.Helper()
	dir := t.TempDir()
	for rel, c := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return safety.FSScanner{}, dir
}

func TestFSScannerContract(t *testing.T) { safetytest.ScannerContract(t, fsFactory) }
func TestFakeScannerContract(t *testing.T) {
	safetytest.ScannerContract(t, safetytest.FakeScannerFactory)
}

// linkFactory builds a repo with files, then makes each dir in env.Links a
// link (junction on Windows) to a folder that holds an agent settings file,
// and makes each file in env.Unreadable unreadable.
func linkFactory(t *testing.T, env safetytest.ScannerEnv) (safety.ConfigScanner, string) {
	t.Helper()
	s, dir := fsFactory(t, env.Files)
	for _, u := range env.Unreadable {
		denyRead(t, filepath.Join(dir, filepath.FromSlash(u)))
	}
	for _, l := range env.Links {
		target := t.TempDir()
		if err := os.WriteFile(filepath.Join(target, "settings.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, filepath.FromSlash(l))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		mkLink(t, link, target)
	}
	return s, dir
}

func TestFSLinkedConfigContract(t *testing.T) { safetytest.LinkScannerContract(t, linkFactory) }
func TestFakeLinkedConfigContract(t *testing.T) {
	safetytest.LinkScannerContract(t, safetytest.FakeLinkScannerFactory)
}

func TestScanSkipsDependencyFolders(t *testing.T) {
	files := map[string]string{"CLAUDE.md": "y"}
	for _, d := range []string{"node_modules", "target", "build", "dist", ".venv", "vendor", "Node_Modules"} {
		files[d+"/pkg/CLAUDE.md"] = "third party"
	}
	s, dir := fsFactory(t, files)
	sc, err := s.Scan(context.Background(), dir, nil, []string{"CLAUDE.md"})
	if err != nil || len(sc.Files) != 1 || sc.Files[0].Path != "CLAUDE.md" {
		t.Fatalf("scan = %+v, %v", sc, err)
	}
}

func TestScanSkippedFolderNamedByConfigPathIsRead(t *testing.T) {
	s, dir := fsFactory(t, map[string]string{"build/agents/a.md": "x", "dist/other.md": "y"})
	sc, err := s.Scan(context.Background(), dir, []string{"build/agents"}, nil)
	if err != nil || len(sc.Files) != 1 || sc.Files[0].Path != "build/agents/a.md" {
		t.Fatalf("scan = %+v, %v", sc, err)
	}
}

func TestScanSkipsGitDir(t *testing.T) {
	s, dir := fsFactory(t, map[string]string{".git/CLAUDE.md": "x", "CLAUDE.md": "y"})
	sc, err := s.Scan(context.Background(), dir, nil, []string{"CLAUDE.md"})
	if err != nil || len(sc.Files) != 1 || sc.Files[0].Path != "CLAUDE.md" {
		t.Fatalf("scan = %+v, %v", sc, err)
	}
}

func TestScanFoldsCase(t *testing.T) {
	s, dir := fsFactory(t, map[string]string{"sub/claude.MD": "y"})
	sc, err := s.Scan(context.Background(), dir, nil, []string{"CLAUDE.md"})
	if err != nil || sc.Empty() {
		t.Fatalf("scan = %+v, %v", sc, err)
	}
}

func TestScanLinkedConfigFolderRefused(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, ".claude")); err != nil {
		t.Skip("symlinks not available: " + err.Error())
	}
	_, err := safety.Scan(context.Background(), dir, []string{".claude"}, nil)
	if err == nil || !strings.Contains(err.Error(), ".claude") {
		t.Fatalf("err = %v", err)
	}
}

func TestScanInvalidSettingsJSONIsOpaque(t *testing.T) {
	s, dir := fsFactory(t, map[string]string{".mcp.json": "{nope"})
	sc, err := s.Scan(context.Background(), dir, []string{".mcp.json"}, nil)
	if err != nil || len(sc.Files) != 1 || !sc.Files[0].Opaque {
		t.Fatalf("scan = %+v, %v", sc, err)
	}
}

func TestScanRedactsTokensInSummary(t *testing.T) {
	s, dir := fsFactory(t, map[string]string{".mcp.json": `{"mcpServers":{"x":{"url":"https://user:hunter2pass@example.test/mcp"}}}`})
	sc, err := s.Scan(context.Background(), dir, []string{".mcp.json"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(sc.Files[0].WouldRun, " "), "hunter2pass") {
		t.Fatalf("credential in summary: %v", sc.Files[0].WouldRun)
	}
}

func TestScanCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, dir := fsFactory(t, map[string]string{"CLAUDE.md": "x"})
	if _, err := safety.Scan(ctx, dir, nil, []string{"CLAUDE.md"}); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
}
