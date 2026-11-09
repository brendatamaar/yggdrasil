package terminal_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
	tt "github.com/brendatamaar/yggdrasil/internal/terminal/terminaltest"
)

var hostSecrets = map[string]string{
	"GITHUB_TOKEN":          "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"GITLAB_TOKEN":          "glpat-bbbbbbbbbbbbbbbbbbbb",
	"YGG_API_KEY":           "sk-cccccccccccccccccccc",
	"AWS_SECRET_ACCESS_KEY": "dddddddddddddddddddd",
	"NPM_TOKEN":             "eeeeeeeeeeeeeeeeeeee",
	"ANTHROPIC_API_KEY":     "sk-ant-ffffffffffffffffffff",
	"VIRTUAL_ENV":           "/venv-leak",
	"PYTHONPATH":            "/pp-leak",
	"CONDA_PREFIX":          "/conda-leak",
}

// Invariant 3: the shell the terminal starts gets the clean environment: none
// of the host's credentials, and the single-use socket token is not in it.
func TestInvariant_3_terminal_shell_gets_no_token_vars(t *testing.T) {
	for k, v := range hostSecrets {
		t.Setenv(k, v)
	}
	f := newFixture(t)
	tok, _, err := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	if err != nil {
		t.Fatal(err)
	}
	se, err := f.svc.Redeem(context.Background(), tok, ct.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	sock := make(chan error, 1)
	go func() { sock <- se.Run(context.Background(), tt.NewMemSocket()) }()
	f.pty(t, 0).Exit(0)
	_ = finished(t, sock)

	spec := f.starter.Specs()[0]
	if spec.Env == nil {
		t.Fatal("the shell got a nil environment (it would inherit the host's)")
	}
	all := strings.Join(spec.Env, "\n") + "\n" + strings.Join(spec.Argv, " ") + "\n" + spec.Dir
	for k, v := range hostSecrets {
		if strings.Contains(all, v) || strings.Contains(strings.ToUpper(all), k+"=") {
			t.Errorf("%s reached the shell", k)
		}
	}
	if strings.Contains(all, tok) {
		t.Error("the terminal's own socket token reached the shell")
	}
}

// Invariant 4: the raw socket token is never stored. Every audit entry of a
// full session (open, close) and every refusal message is free of it, and of
// the folder path (entries carry IDs).
func TestInvariant_4_terminal_token_never_in_audit_or_messages(t *testing.T) {
	f := newFixture(t)
	tok, _, err := f.svc.Issue(context.Background(), ct.Target{Repo: "repo_a"})
	if err != nil {
		t.Fatal(err)
	}
	sock := tt.NewMemSocket()
	se, err := f.svc.Redeem(context.Background(), tok, ct.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- se.Run(context.Background(), sock) }()
	f.pty(t, 0).Output("typed " + tok + "\r\n")
	f.pty(t, 0).Exit(2)
	_ = finished(t, done)

	// A reused token is refused; its message must not echo the token.
	_, rerr := f.svc.Redeem(context.Background(), tok, ct.Size{Cols: 80, Rows: 24})
	var re *terminal.RefusedError
	if !errors.As(rerr, &re) {
		t.Fatalf("reuse: %v", rerr)
	}
	texts := []string{re.Error(), re.Message()}
	if len(f.audit.Entries()) < 2 {
		t.Fatalf("expected open and close entries, got %v", f.audit.Entries())
	}
	for _, e := range f.audit.Entries() {
		texts = append(texts, fmt.Sprintf("%+v %#v", e, e))
	}
	joined := strings.Join(texts, "\n")
	if strings.Contains(joined, tok) || strings.Contains(joined, string(ct.HashToken(tok))) {
		t.Errorf("the token (or its hash) is in an audit entry or message:\n%s", joined)
	}
	if strings.Contains(joined, f.dir) {
		t.Errorf("the folder path is in an audit entry:\n%s", joined)
	}
}

// Invariant 6: a terminal opens only in a tracked folder, checked at issue and
// again at redeem; a folder removed in between starts no shell.
func TestInvariant_6_terminal_refuses_untracked_folders(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, _, err := f.svc.Issue(ctx, ct.Target{Repo: "repo_not_tracked"}); err == nil {
		t.Fatal("a token was issued for an untracked repo")
	}
	if _, _, err := f.svc.Issue(ctx, ct.Target{Worktree: "wt_not_tracked"}); err == nil {
		t.Fatal("a token was issued for an unknown worktree")
	}
	tok, _, err := f.svc.Issue(ctx, ct.Target{Worktree: "wt_1"})
	if err != nil {
		t.Fatal(err)
	}
	f.folders.RemoveRepo("repo_a") // removed between issue and redeem
	if se, err := f.svc.Redeem(ctx, tok, ct.Size{Cols: 80, Rows: 24}); err == nil {
		se.Abort()
		t.Fatal("a session opened in a folder that is no longer tracked")
	}
	if n := len(f.starter.PTYs()); n != 0 {
		t.Fatalf("%d shells started", n)
	}
}
