package proposals

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// WS-09, MCP-05: an agent proposes ignore entries without knowing the file's
// checksum. Create pins the current .gitignore (read only) before the action is
// hashed, so what the user confirms includes it, and nothing is written.
func TestAgentIgnoreProposalPinsTheCurrentFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		present bool
	}{
		{"existing file", "*.log\n", true},
		{"no file yet", "", false},
		{"crlf file", "*.log\r\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newP6(t)
			explicit := x.ignoreAction(tc.content, tc.present, "/dist/")
			bare := explicit
			bare.ExpectedHash, bare.ExpectedAbsent = "", false

			v, err := x.svc.Create(x.ctx, CreateRequest{Task: x.task.ID, Action: bare, Source: coreprop.AgentSource{Task: x.task.ID}})
			if err != nil {
				t.Fatal(err)
			}
			if v.Status != core.ProposalPending || !v.FromAgent {
				t.Fatalf("view %+v", v)
			}
			if x.ign.Appends() != 0 || x.ign.Reads() != 1 {
				t.Fatalf("creating the proposal read %d times and wrote %d times", x.ign.Reads(), x.ign.Appends())
			}
			// The same action with the checksum given by hand has the same hash.
			if w, err := x.svc.Create(x.ctx, CreateRequest{Task: x.task.ID, Action: explicit}); err != nil || w.ActionHash != v.ActionHash {
				t.Fatalf("pinned hash differs: %v %+v vs %+v", err, w, v)
			}
		})
	}
}

// A checksum the caller gave is kept: nothing is read.
func TestIgnoreChecksumFromTheCallerIsKept(t *testing.T) {
	x := newP6(t)
	a := x.ignoreAction("*.log\n", true)
	before := x.ign.Reads()
	if _, err := x.svc.Create(x.ctx, CreateRequest{Task: x.task.ID, Action: a}); err != nil {
		t.Fatal(err)
	}
	if x.ign.Reads() != before {
		t.Fatal("the file was read although the checksum was given")
	}
}

func TestAgentIgnoreProposalWithoutThePortOrAReadableFileIsRefused(t *testing.T) {
	x := newP6(t)
	bare := x.ignoreAction("*.log\n", true)
	bare.ExpectedHash = ""
	agent := coreprop.AgentSource{Task: x.task.ID}

	x.cfg.Ignore = unreadableIgnore{x.ign}
	x.svc.Close()
	x.start()
	_, err := x.svc.Create(x.ctx, CreateRequest{Task: x.task.ID, Action: bare, Source: agent})
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if got := r.UserMessage(); got == "" || containsAny(got, `C:\`, "access denied") {
		t.Fatalf("message %q leaks the error", got)
	}
	if n, _ := x.svc.PendingCount(x.ctx, x.task.ID); n != 0 {
		t.Fatalf("%d proposals stored", n)
	}

	w := newWorld(t) // no ignore port
	_, err = w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Source: coreprop.AgentSource{Task: w.task.ID},
		Action: coreprop.IgnoreEntries{Where: coreprop.MainCheckout{Repo: w.repo.ID}, Entries: []string{"/dist/"}}})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("without the port: %v", err)
	}
}

// unreadableIgnore fails the read the way the platform writer does for a locked
// file, with an error that holds a machine path.
type unreadableIgnore struct{ platform.IgnoreWriter }

func (unreadableIgnore) ReadIgnore(context.Context, string) (platform.IgnoreSnapshot, error) {
	return platform.IgnoreSnapshot{}, errors.New("open C:\\Users\\dev\\repo\\.gitignore: access denied")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}
