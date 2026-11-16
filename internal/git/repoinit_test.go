package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Invariants 3 and 4: the commit and init helpers use the same process path as
// every other git call, so a git child there gets the clean environment (a
// shell alias prints what the child really received), and a credential URL is
// refused before any child is started.
func TestInvariant_3_NewRepoHelpersStartCleanChildren(t *testing.T) {
	secrets := map[string]string{
		"GITHUB_TOKEN": "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"GITLAB_TOKEN": "glpat-bbbbbbbbbbbbbbbbbbbb",
		"YGG_API_KEY":  "sk-cccccccccccccccccccc",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"GIT_CONFIG_GLOBAL": filepath.Join(t.TempDir(), "none"), "GIT_CONFIG_NOSYSTEM": "1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	g := New0(t, env)
	dir := t.TempDir()
	// execWith is the path of CommitFirst's add, author check and commit.
	out, _, code, err := g.execWith(context.Background(), dir, "printenv", nil, false, "-c", "alias.printenv=!env", "printenv")
	if err != nil || code != 0 {
		t.Skipf("this git cannot run a shell alias: %v (exit %d)", err, code)
	}
	if !strings.Contains(out, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("the child did not print its environment:\n%s", out)
	}
	for k, v := range secrets {
		if strings.Contains(out, v) || strings.Contains(strings.ToUpper(out), k+"=") {
			t.Errorf("%s reached a git child", k)
		}
	}

	// A credential URL is refused before a process is started: with no git
	// executable at all the error is still the URL error, not a start error.
	g.exe = filepath.Join(dir, "no-such-git")
	err = g.AddOrigin(context.Background(), dir, "https://user:ghp_secrettoken@git.example.test/acme/other")
	if !errors.Is(err, ErrCredentialURL) {
		t.Fatalf("err = %v, want ErrCredentialURL before any child", err)
	}
	if strings.Contains(err.Error()+UserMessage(err), "ghp_secrettoken") {
		t.Fatal("the URL credential is echoed")
	}
}

func TestInitRepoDoesNotCopyTemplateHooks(t *testing.T) {
	tmpl := filepath.Join(t.TempDir(), "template", "hooks")
	if err := os.MkdirAll(tmpl, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpl, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { //nolint:gosec // test hook
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "new repo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	// init.templateDir would copy the template in a plain `git init`.
	cfg := filepath.Join(t.TempDir(), "cfg")
	if err := os.WriteFile(cfg, []byte("[init]\n\ttemplateDir = "+filepath.ToSlash(filepath.Dir(tmpl))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{"GIT_CONFIG_GLOBAL": cfg, "GIT_CONFIG_NOSYSTEM": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	g := New0(t, env)
	if err := g.InitRepo(bg(), dir, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "hooks", "pre-commit")); err == nil {
		t.Fatal("the template hook was copied into the new repo")
	}
}

func TestReadFirstCommitRefusesHistory(t *testing.T) {
	f := newFx(t)
	dir := f.repo("acme shop api")
	f.commit(dir, "README.md", "one\n", "first")
	f.commit(dir, "more.txt", "two\n", "second")
	_, found, err := f.g.ReadFirstCommit(bg(), dir, "main")
	if found || !errors.Is(err, ErrNotFirstCommit) {
		t.Fatalf("found %v, err %v; want ErrNotFirstCommit", found, err)
	}
	if _, found, err := f.g.ReadFirstCommit(bg(), dir, "nope"); found || err != nil {
		t.Fatalf("unknown branch: found %v, err %v", found, err)
	}
	if _, _, err := f.g.ReadFirstCommit(bg(), dir, "-x"); err == nil {
		t.Fatal("option-looking branch accepted")
	}
}

func TestOriginWithStoredCredentialIsNeverEchoed(t *testing.T) {
	f := newFx(t)
	dir := f.repo("acme shop api")
	f.git(dir, "remote", "add", "origin", "https://user:hunter2secret@git.example.test/acme/shop-api")
	url, exists, err := f.g.Origin(bg(), dir)
	if err == nil || url != "" || !exists {
		t.Fatalf("Origin = %q, %v, %v; want an error", url, exists, err)
	}
	if strings.Contains(err.Error()+UserMessage(err), "hunter2secret") {
		t.Fatalf("the stored secret is echoed: %v", err)
	}
	if err := f.g.AddOrigin(bg(), dir, "https://git.example.test/acme/shop-api"); err == nil {
		t.Fatal("AddOrigin over a credential origin succeeded")
	}
}

func TestMessageHashMatchesStoredMessage(t *testing.T) {
	f := newFx(t)
	dir := f.repo("acme shop api")
	f.write(dir, "README.md", "x\n")
	msg := "  Initial commit\n\nWith a body.   \n\n"
	fc, err := f.g.CommitFirst(bg(), dir, []string{"README.md"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	if fc.MessageHash != MessageHash(msg) || fc.MessageHash != MessageHash(strings.TrimSpace(msg)) {
		t.Fatalf("message hash %s, want %s", fc.MessageHash, MessageHash(msg))
	}
}
