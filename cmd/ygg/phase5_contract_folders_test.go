package main

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
	"github.com/brendatamaar/yggdrasil/internal/terminal/terminaltest"
)

// The live Folders adapter passes the same suite as the fake: only tracked
// folders resolve, a worktree of another repo is refused, a removed repo is
// untracked at the redeem-time re-check (invariant 6).
func TestPhase5ContractTerminalFolders(t *testing.T) {
	terminaltest.FoldersContract(t, func() terminaltest.FoldersFixture {
		w := newP5World(t)
		return terminaltest.FoldersFixture{
			Folders:     terminalFolders{w.dirs},
			RemoveRepoA: func() { dbtest.UntrackRepo(t, w.store, w.repoA) },
		}
	})
}

// The live Audit sink passes the same suite as the in-memory one: entries are
// stored, in order, with the same fields read back.
func TestPhase5ContractTerminalAudit(t *testing.T) {
	terminaltest.AuditContract(t, func() (terminal.AuditSink, func() []terminal.AuditEntry) {
		w := newP5World(t)
		sink := terminalAudit{store: w.store, red: w.redactor}
		return sink, func() []terminal.AuditEntry {
			rows, err := w.store.ListAudit(context.Background(), "terminal:ts_1", 100)
			if err != nil {
				t.Fatal(err)
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].At.Before(rows[j].At) })
			var out []terminal.AuditEntry
			for _, r := range rows {
				var d struct {
					Repo, Worktree string
					Cause          ct.CloseCause
					Code           int
				}
				if err := json.Unmarshal(r.Detail, &d); err != nil {
					t.Fatal(err)
				}
				out = append(out, terminal.AuditEntry{
					Action: ct.AuditAction(r.Action), Session: "ts_1",
					Folder: ct.Folder{Repo: core.RepoID(d.Repo), Worktree: core.WorktreeID(d.Worktree)},
					Cause:  d.Cause, Code: d.Code, At: r.At.UTC(),
				})
			}
			return out
		}
	})
}

// Invariants 4 and 8: each terminal audit row is written by the actor "user",
// holds exactly the ID, cause and code keys, and no free text.
func TestPhase5ContractTerminalAuditRowShape(t *testing.T) {
	w := newP5World(t)
	ctx := context.Background()
	sink := terminalAudit{store: w.store, red: w.redactor}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const tok = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	// Even a cause that somehow carried a token is redacted on the way in.
	if err := sink.Record(ctx, terminal.AuditEntry{Action: ct.AuditClose, Session: "ts_9", Folder: ct.Folder{Repo: "repo_a"}, Cause: ct.CloseCause(tok), At: at}); err != nil {
		t.Fatal(err)
	}
	rows, err := w.store.ListAudit(ctx, "terminal:ts_9", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v, err %v", rows, err)
	}
	if rows[0].Actor != "user" || rows[0].Action != string(ct.AuditClose) {
		t.Errorf("row = %+v", rows[0])
	}
	var d map[string]any
	if err := json.Unmarshal(rows[0].Detail, &d); err != nil {
		t.Fatal(err)
	}
	if len(d) != 4 {
		t.Errorf("detail keys = %v, want repo, worktree, cause, code", d)
	}
	if string(rows[0].Detail) != "" && contains(string(rows[0].Detail), tok) {
		t.Errorf("the token is in the stored detail: %s", rows[0].Detail)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
