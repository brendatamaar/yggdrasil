package hosts_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func TestReloadAddsNewTokenToTheProcessRedactor(t *testing.T) {
	const fresh = "plainsecretvalue42"
	red := safety.NewRedactor()
	svc := hosts.New(hosts.Deps{
		Config: hosts.ParseConfig(map[string]hosts.Var{}),
		Reload: func() (hosts.Config, error) {
			vars, _ := hosts.ParseEnvText("YGG_HOST_1=gitlab https://git.acme.dev\nYGG_HOST_1_TOKEN=" + fresh + "\n")
			return hosts.ParseConfig(vars), nil
		},
		Secrets: red,
	})
	// No pattern catches this value: only an exact add can mask it.
	if got := red.Redact("see " + fresh + " here"); !strings.Contains(got, fresh) {
		t.Fatalf("test setup: the value is masked before the reload: %q", got)
	}
	if err := svc.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := red.Redact("see " + fresh + " here"); strings.Contains(got, fresh) {
		t.Fatalf("a reloaded token is not redacted: %q", got)
	}
}

func TestRedactorAddIsConcurrencySafe(t *testing.T) {
	r := safety.NewRedactor()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			r.Add("secret-value-" + strings.Repeat("x", i%7))
		}
	}()
	for i := 0; i < 200; i++ {
		_ = r.Redact("secret-value-xxx")
	}
	<-done
	if got := r.Redact("a secret-value-xxx b"); strings.Contains(got, "secret-value-xxx") {
		t.Fatalf("not redacted: %q", got)
	}
}

// hangingRemotes blocks until its context ends, like a git that hangs.
type hangingRemotes struct{}

func (hangingRemotes) RemoteURL(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestResolveGivesUpOnAHungGit(t *testing.T) {
	svc := hosts.New(hosts.Deps{
		Config:        hosts.ParseConfig(map[string]hosts.Var{}),
		Remotes:       hangingRemotes{},
		Log:           slog.New(slog.DiscardHandler),
		RemoteTimeout: 30 * time.Millisecond,
		TickEvery:     5 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); svc.Close() })
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	svc.SetRepos([]hosts.RepoRef{{ID: "r1", Path: "p1", Branch: "main"}})
	eventually(t, "the hung remote read gives up", func() bool {
		v, _ := svc.RepoHost(context.Background(), "r1")
		return !v.Linked && strings.Contains(v.NotLinked, "couldn't read")
	})
}
