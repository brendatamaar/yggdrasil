// Package proposalstest holds the contract suites of the capabilities the
// proposals service uses (docs/design/proposals.md): the host classifier and
// the install-build runner. Each is run against a fake and against the real
// adapter in cmd/ygg. It uses plain strings so that internal/proposals tests
// can import it without a cycle.
package proposalstest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Host kinds as strings (the values of proposals.HostKind).
const (
	GitLab  = "gitlab"
	GitHub  = "github"
	Gitea   = "gitea"
	Unknown = "unknown"
)

// HostsFactory returns a classifier for remote URLs given the configured
// instances: host name to kind.
type HostsFactory func(instances map[string]string) func(remoteURL string) string

// HostsContract: a remote belongs to the configured instance whose host name
// it names, in every URL form git accepts; anything else is Unknown; a
// credential in the URL changes nothing and nothing panics.
func HostsContract(t *testing.T, mk HostsFactory) {
	t.Helper()
	inst := map[string]string{"git.acme.dev": GitLab, "github.com": GitHub, "code.example.org": Gitea}
	cases := []struct{ name, url, want string }{
		{"https", "https://git.acme.dev/acme/shop-api.git", GitLab},
		{"scp form", "git@git.acme.dev:acme/shop-api.git", GitLab},
		{"credentials in the URL", "https://user:tok@github.com/acme/shop-api.git", GitHub},
		{"host name case", "https://GitHub.com/acme/shop-api.git", GitHub},
		{"ssh with port", "ssh://git@code.example.org:2222/acme/shop-api.git", Gitea},
		{"unconfigured host", "https://elsewhere.example/acme/shop-api.git", Unknown},
		{"configured name only as a path part", "https://elsewhere.example/git.acme.dev/shop-api.git", Unknown},
		{"configured name only as a suffix", "https://evil-git.acme.dev.example/acme/shop-api.git", Unknown},
		{"not a URL", "not a url", Unknown},
		{"empty", "", Unknown},
		{"windows path", `C:\repos\shop-api`, Unknown},
		{"local path", "/srv/git/shop-api.git", Unknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mk(inst)(c.url); got != c.want {
				t.Errorf("Kind(%q) = %q, want %q", c.url, got, c.want)
			}
		})
	}
	t.Run("no instances configured", func(t *testing.T) {
		if got := mk(nil)("https://git.acme.dev/acme/shop-api.git"); got != Unknown {
			t.Errorf("Kind = %q, want %q", got, Unknown)
		}
	})
	t.Run("a token never changes the answer", func(t *testing.T) {
		k := mk(inst)
		base := k("https://git.acme.dev/acme/shop-api.git")
		if got := k("https://oauth2:glpat-SENTINEL@git.acme.dev/acme/shop-api.git"); got != base {
			t.Errorf("with token %q, without %q", got, base)
		}
	})
}

// FakeHostsFactory is the fake classifier.
func FakeHostsFactory(instances map[string]string) func(string) string {
	return func(raw string) string {
		host := ""
		if i := strings.Index(raw, "://"); i >= 0 {
			if u, err := url.Parse(raw); err == nil {
				host = u.Hostname()
			}
		} else if at := strings.Index(raw, "@"); at >= 0 && !strings.ContainsAny(raw, ` \`) {
			rest := raw[at+1:]
			if c := strings.Index(rest, ":"); c > 0 {
				host = rest[:c]
			}
		}
		for h, k := range instances {
			if host != "" && strings.EqualFold(h, host) {
				return k
			}
		}
		return Unknown
	}
}

// BuildsRig is the build runner under test with the means to drive its
// builds. Results are "succeeded", "failed", "cancelled" and "running".
type BuildsRig struct {
	// Step is an install step the repo has.
	Step string
	// Enqueue queues the install step and returns the build ID.
	Enqueue func(ctx context.Context, step, owner string) (string, error)
	// Wait blocks until the build ends.
	Wait func(ctx context.Context, id string) (succeeded bool, reason string, err error)
	// SetResult decides how every build ends (or that it does not).
	SetResult func(result string)
	// Queued lists the owners of the builds queued so far.
	Queued func() []string
}

// BuildsContract: only a step the repo has is queued, once, in the proposal's
// name; Wait reports success, or a plain reason for a failed or cancelled
// build, and stops with the context when the build never ends.
func BuildsContract(t *testing.T, mk func(t *testing.T) BuildsRig) {
	t.Helper()
	t.Run("an unknown step is refused and nothing is queued", func(t *testing.T) {
		r := mk(t)
		if _, err := r.Enqueue(context.Background(), "no-such-step", "proposal:prp_1"); err == nil {
			t.Fatal("an unknown install step was queued")
		}
		if n := len(r.Queued()); n != 0 {
			t.Errorf("%d builds queued", n)
		}
	})
	t.Run("a known step is queued once in the proposal's name", func(t *testing.T) {
		r := mk(t)
		id, err := r.Enqueue(context.Background(), r.Step, "proposal:prp_1")
		if err != nil || id == "" {
			t.Fatalf("enqueue = %q, %v", id, err)
		}
		if q := r.Queued(); len(q) != 1 || q[0] != "proposal:prp_1" {
			t.Errorf("queued owners = %v", q)
		}
	})
	for _, c := range []struct {
		result string
		ok     bool
	}{{"succeeded", true}, {"failed", false}, {"cancelled", false}} {
		t.Run("wait reports "+c.result, func(t *testing.T) {
			r := mk(t)
			id, err := r.Enqueue(context.Background(), r.Step, "proposal:prp_1")
			if err != nil {
				t.Fatal(err)
			}
			r.SetResult(c.result)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ok, reason, err := r.Wait(ctx, id)
			if err != nil || ok != c.ok || (!c.ok && strings.TrimSpace(reason) == "") {
				t.Errorf("outcome ok=%v reason=%q err=%v", ok, reason, err)
			}
		})
	}
	t.Run("wait stops with the context", func(t *testing.T) {
		r := mk(t)
		id, err := r.Enqueue(context.Background(), r.Step, "proposal:prp_1")
		if err != nil {
			t.Fatal(err)
		}
		r.SetResult("running")
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { _, _, err := r.Wait(ctx, id); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Error("Wait returned success for a build that never ended")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Wait ignored its context")
		}
	})
}

// FakeBuilds returns the fake runner.
func FakeBuilds(t *testing.T) BuildsRig {
	f := &fakeBuilds{result: "running"}
	return BuildsRig{
		Step: "install",
		Enqueue: func(_ context.Context, step, owner string) (string, error) {
			if step != "install" {
				return "", errors.New("the repo has no such install step")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.owners = append(f.owners, owner)
			return fmt.Sprintf("bld_%d", len(f.owners)), nil
		},
		Wait: func(ctx context.Context, _ string) (bool, string, error) {
			for {
				f.mu.Lock()
				r := f.result
				f.mu.Unlock()
				switch r {
				case "succeeded":
					return true, "", nil
				case "failed":
					return false, "The install command failed.", nil
				case "cancelled":
					return false, "The build was cancelled.", nil
				}
				select {
				case <-ctx.Done():
					return false, "", ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}
		},
		SetResult: func(r string) { f.mu.Lock(); f.result = r; f.mu.Unlock() },
		Queued: func() []string {
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]string(nil), f.owners...)
		},
	}
}

type fakeBuilds struct {
	mu     sync.Mutex
	owners []string
	result string
}
