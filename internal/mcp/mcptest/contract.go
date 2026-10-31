package mcptest

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/mcp"
)

// SessionRegistry is the session capability (docs/design/mcp.md).
type SessionRegistry interface {
	Issue(task core.TaskID, attempt int) (string, error)
	Lookup(token string) (mcp.Active, bool)
	Revoke(task core.TaskID)
}

// SessionsContract is the contract of SessionRegistry. Every implementation
// must pass it (M6).
func SessionsContract(t *testing.T, mk func() SessionRegistry) {
	t.Helper()
	t.Run("a token works for its own task only", func(t *testing.T) {
		r := mk()
		a, _ := r.Issue("t1", 1)
		b, _ := r.Issue("t2", 3)
		if got, ok := r.Lookup(a); !ok || got.Task != "t1" || got.Attempt != 1 {
			t.Errorf("t1 token: %+v %v", got, ok)
		}
		if got, ok := r.Lookup(b); !ok || got.Task != "t2" || got.Attempt != 3 {
			t.Errorf("t2 token: %+v %v", got, ok)
		}
	})
	t.Run("two tasks get different tokens", func(t *testing.T) {
		r := mk()
		a, _ := r.Issue("t1", 1)
		b, _ := r.Issue("t2", 1)
		if a == b || a == "" {
			t.Errorf("tokens %q %q", a, b)
		}
	})
	t.Run("a new run gets a new token and the old one stops", func(t *testing.T) {
		r := mk()
		a, _ := r.Issue("t1", 1)
		b, _ := r.Issue("t1", 2)
		if _, ok := r.Lookup(a); ok {
			t.Error("the old token still works")
		}
		if _, ok := r.Lookup(b); !ok {
			t.Error("the new token does not work")
		}
	})
	t.Run("a revoked token fails", func(t *testing.T) {
		r := mk()
		a, _ := r.Issue("t1", 1)
		b, _ := r.Issue("t2", 1)
		r.Revoke("t1")
		if _, ok := r.Lookup(a); ok {
			t.Error("revoked token works")
		}
		if _, ok := r.Lookup(b); !ok {
			t.Error("revoking t1 revoked t2")
		}
	})
	t.Run("unknown, empty and near-miss tokens fail", func(t *testing.T) {
		r := mk()
		a, _ := r.Issue("t1", 1)
		for _, tok := range []string{"", "x", a[:len(a)-1], a + "x", a[1:]} {
			if _, ok := r.Lookup(tok); ok {
				t.Errorf("token %q authenticated", tok)
			}
		}
	})
	t.Run("revoking an unknown task does nothing", func(t *testing.T) {
		r := mk()
		a, _ := r.Issue("t1", 1)
		r.Revoke("nobody")
		if _, ok := r.Lookup(a); !ok {
			t.Error("a revoke of another task ended t1")
		}
	})
	t.Run("an invalid task id is refused", func(t *testing.T) {
		if _, err := mk().Issue("", 1); err == nil {
			t.Error("issued a token for an empty task")
		}
	})
}

// DevServersContract is the contract of mcp.DevServers: with none running the
// list is empty and a log request is ErrNoDevServer.
func DevServersContract(t *testing.T, mk func() mcp.DevServers) {
	t.Helper()
	d := mk()
	list, err := d.List(context.Background(), "p1")
	if err != nil || len(list) != 0 {
		t.Errorf("List = %v, %v; want none", list, err)
	}
	if _, err := d.Log(context.Background(), "p1", "vite", 100); !errors.Is(err, mcp.ErrNoDevServer) {
		t.Errorf("Log of an unknown server = %v, want ErrNoDevServer", err)
	}
}
