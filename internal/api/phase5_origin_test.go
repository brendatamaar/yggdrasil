package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
)

type countingTerm struct{ calls atomic.Int32 }

func (c *countingTerm) Issue(context.Context, ct.Target) (string, time.Duration, error) {
	c.calls.Add(1)
	return "x", 30 * time.Second, nil
}

func (c *countingTerm) Redeem(context.Context, string, ct.Size) (*terminal.Session, error) {
	c.calls.Add(1)
	return nil, &terminal.RefusedError{Reason: ct.RefusedUnknown}
}

// Invariant 7: every Phase 5 endpoint (dev servers, worktree environment,
// terminal token and socket) keeps the Host and Origin checks. A request with a
// foreign Host or Origin gets 403 and never reaches the service; the same
// request from the page itself does reach it.
func TestInvariant_7_phase5_endpoints_check_host_and_origin(t *testing.T) {
	dev, wenv, term := &fakeDev{}, &fakeEnv{}, &countingTerm{}
	e := newEnv(t, func(d *Deps) {
		d.Phase5 = Phase5Services{DevServers: dev, WorktreeEnv: wenv, Terminal: term}
	})
	id := string(devservers.IDOf(dsc.Key{Repo: "repo_a", Worktree: "wt_1", CommandKey: "dev"}))
	calls := func() int {
		dev.mu.Lock()
		defer dev.mu.Unlock()
		wenv.mu.Lock()
		defer wenv.mu.Unlock()
		return len(dev.calls) + len(wenv.set) + len(wenv.copy) + int(term.calls.Load())
	}
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/devservers", nil},
		{"POST", "/api/devservers/" + id + "/start", nil},
		{"POST", "/api/devservers/" + id + "/stop", nil},
		{"POST", "/api/devservers/" + id + "/restart", nil},
		{"GET", "/api/devservers/" + id + "/log", nil},
		{"GET", "/api/repos/repo_a/env", nil},
		{"PUT", "/api/repos/repo_a/env", RepoEnvDTO{RepoID: core.RepoID("repo_a")}},
		{"GET", "/api/worktrees/wt_1/env", nil},
		{"POST", "/api/worktrees/wt_1/profile/copy", nil},
		{"POST", "/api/terminal", TerminalTokenRequest{RepoID: "repo_a"}},
		{"GET", "/ws/terminal?token=" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil},
	}
	hostile := map[string]func(*http.Request){
		"foreign host":           func(r *http.Request) { r.Host = "evil.example" },
		"loopback look-alike":    func(r *http.Request) { r.Host = "127.0.0.1.evil.example" },
		"localhost suffix":       func(r *http.Request) { r.Host = "localhost.evil.com" },
		"foreign origin":         func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"opaque origin":          func(r *http.Request) { r.Header.Set("Origin", "null") },
		"origin on another port": func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:1") },
	}
	for _, rt := range routes {
		for name, mod := range hostile {
			before := calls()
			resp, _ := e.do(t, rt.method, rt.path, rt.body, mod)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s with a %s: status %d, want 403", rt.method, rt.path, name, resp.StatusCode)
			}
			if calls() != before {
				t.Errorf("%s %s with a %s reached the service", rt.method, rt.path, name)
			}
		}
		// The control: without the hostile header the request is not a 403.
		if resp, _ := e.do(t, rt.method, rt.path, rt.body); resp.StatusCode == http.StatusForbidden {
			t.Errorf("%s %s: the plain request was refused", rt.method, rt.path)
		}
	}
	if calls() == 0 {
		t.Fatal("no plain request reached a service: the test cannot show a hostile one would")
	}
}

// ParseToken rule "extra query parameters carrying paths": a path, dir or cwd
// in the socket URL never changes where the shell starts; the folder is the
// one the token was issued for (invariant 6).
func TestTerminalSocketIgnoresPathParameters(t *testing.T) {
	te := newTermEnv(t)
	tok := te.token(t, TerminalTokenRequest{RepoID: "repo_a"})
	c, resp, err := te.dialTerm(tok + "&path=/etc&dir=/&cwd=C:%5CWindows")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	closeBody(resp)
	defer c.CloseNow() //nolint:errcheck // test cleanup
	waitFor(t, "the shell to start", func() bool { return len(te.starter.Specs()) == 1 })
	spec := te.starter.Specs()[0]
	if spec.Dir == "" || spec.Dir == "/etc" || spec.Dir == "/" || spec.Dir == `C:\Windows` {
		t.Fatalf("the shell started in %q", spec.Dir)
	}
}
