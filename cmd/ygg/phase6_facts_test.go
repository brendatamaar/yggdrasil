package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// errHostCache fails both host reads.
type errHostCache struct{ err error }

func (e errHostCache) RepoHost(context.Context, core.RepoID) (api.RepoHostView, error) {
	return api.RepoHostView{}, e.err
}

func (e errHostCache) FailedPipelines(context.Context) (map[core.RepoID]struct{}, error) {
	return nil, e.err
}

// When the live HEAD cannot be read, the last status read is not a pin: the pin
// stays empty and the repo is marked unreadable, so a pull is not pinned to a
// stale head and every offering waits for the folder.
func TestPhase6FactsNeverPinToAStaleHead(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.localCommit(t, p, "dirty.txt")
	r.refresh(t, p)
	scope := &scopeReader{store: r.a.store, status: r.a.status, hosts: errHostCache{err: errors.New("down")}, git: r.a.git}

	f, err := scope.Facts(context.Background(), p.id)
	if err != nil || !f.Readable || f.Head != r.head(t, p) {
		t.Fatalf("facts of a readable repo = %+v, %v; want the live head %s", f, err, r.head(t, p))
	}

	// The folder stops being a repo after the last status read.
	if err := os.Rename(filepath.Join(p.dir, ".git"), filepath.Join(p.dir, ".git-off")); err != nil {
		t.Fatal(err)
	}
	f, err = scope.Facts(context.Background(), p.id)
	if err != nil {
		t.Fatal(err)
	}
	if f.Head != "" || f.Readable {
		t.Fatalf("facts = %+v; the stale snapshot head must not be the pin and the repo must be unreadable", f)
	}
	unreadable := false
	for _, x := range f.Reasons {
		unreadable = unreadable || x == coreactions.ReasonUnreadable
	}
	if !unreadable {
		t.Fatalf("reasons = %v; want unreadable", f.Reasons)
	}
	for _, o := range coreactions.Catalogue(f) {
		if o.Kind != coreactions.ImportIssue && !o.WaitingForFolder {
			t.Fatalf("offering %+v is offered as if the repo could be read", o)
		}
	}
	if pin := coreactions.Pin(f, coreactions.PullAndCheck); pin != "" {
		t.Fatalf("pin = %q, want empty", pin)
	}
}

// A failed host read removes only what needs it: it never invents a reason, and
// the failure is logged with the secret removed.
func TestPhase6FactsHostReadErrorsAreLoggedRedactedAndInventNothing(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	r.refresh(t, p)
	var logs bytes.Buffer
	scope := &scopeReader{
		store: r.a.store, status: r.a.status, git: r.a.git,
		hosts:  errHostCache{err: errors.New("GET failed with token ghp_supersecretvalue123")},
		log:    slog.New(slog.NewTextHandler(&logs, nil)),
		redact: safety.NewRedactor("ghp_supersecretvalue123"),
	}
	f, err := scope.Facts(context.Background(), p.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Reasons) != 0 || f.MRNumber != 0 || f.CIRunID != "" || f.Caps.HostLogs {
		t.Fatalf("facts = %+v; a failed host read must give no host reason, MR or capability", f)
	}
	out := logs.String()
	if !strings.Contains(out, "scope read failed") || strings.Contains(out, "ghp_supersecretvalue123") {
		t.Fatalf("log = %q; want the failure logged without the secret", out)
	}
}
