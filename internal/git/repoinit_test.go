package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// recorder wraps platform.Start and keeps what every child was started with.
type recorder struct {
	mu    sync.Mutex
	specs []platform.Spec
}

func (r *recorder) start(ctx context.Context, s platform.Spec) (platform.Process, error) {
	r.mu.Lock()
	r.specs = append(r.specs, s)
	r.mu.Unlock()
	return platform.Start(ctx, s)
}

// Invariants 3 and 4: the new-repo helpers start git with the clean
// environment, no token in argv or env, hooks off for commits, no template for
// init, and a credential URL is never passed to git at all.
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
		"GIT_AUTHOR_NAME": "Ada Test", "GIT_AUTHOR_EMAIL": "ada@example.test",
		"GIT_COMMITTER_NAME": "Ada Test", "GIT_COMMITTER_EMAIL": "ada@example.test",
	}})
	if err != nil {
		t.Fatal(err)
	}
	g := New0(t, env)
	rec := &recorder{}
	g.start = rec.start
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "acme shop api")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(g.InitRepo(ctx, dir, "main"))
	must(os.WriteFile(filepath.Join(dir, "README.md"), []byte("# x\n"), 0o600))
	_, err = g.CommitFirst(ctx, dir, []string{"README.md"}, "Initial commit")
	must(err)
	must(g.AddOrigin(ctx, dir, "https://git.example.test/acme/shop-api"))
	_, _, err = g.Origin(ctx, dir)
	must(err)
	// A credential URL never reaches a git child.
	n := len(rec.specs)
	if err := g.AddOrigin(ctx, dir, "https://user:ghp_secrettoken@git.example.test/acme/other"); err == nil {
		t.Fatal("credential URL accepted")
	}
	if len(rec.specs) != n {
		t.Fatal("a credential URL started a git child")
	}

	var sawHooksOff, sawNoTemplate bool
	for _, s := range rec.specs {
		all := strings.Join(s.Argv, " ") + "\x00" + strings.Join(s.Env, "\x00")
		for k, v := range secrets {
			if strings.Contains(all, v) || strings.Contains(strings.ToUpper(strings.Join(s.Env, "\n")), k+"=") {
				t.Errorf("%s reached a git child: %v", k, s.Argv)
			}
		}
		if strings.Contains(all, "ghp_secrettoken") {
			t.Error("a URL credential reached a git child")
		}
		if strings.Contains(all, "core.hooksPath=") && strings.Contains(all, "commit") {
			sawHooksOff = true
		}
		if strings.Contains(all, "--template=") {
			sawNoTemplate = true
		}
		if !strings.Contains(all, "credential.helper=") {
			t.Errorf("credential helpers not disabled: %v", s.Argv)
		}
	}
	if !sawHooksOff || !sawNoTemplate {
		t.Fatalf("hooks off: %v, no template: %v", sawHooksOff, sawNoTemplate)
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
