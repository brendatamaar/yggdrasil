package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// sideDB opens a second connection to the app's database file, to inject a
// fault the application cannot see: every INSERT into audit_log aborts.
type sideDB struct {
	t  *testing.T
	db *sql.DB
}

func openSideDB(t *testing.T, dataDir string) *sideDB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dataDir, "ygg.db"))+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &sideDB{t: t, db: d}
}

func (s *sideDB) faultAudit(on bool) {
	s.t.Helper()
	q := `DROP TRIGGER IF EXISTS fault_audit`
	if on {
		q = `CREATE TRIGGER IF NOT EXISTS fault_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'injected audit fault'); END`
	}
	if _, err := s.db.Exec(q); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sideDB) count(table string) int {
	s.t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

// Invariant 8, Phase 4 surface: every state change that Phase 4 adds is
// committed in the same transaction as its audit entry. With the audit insert
// made to fail, the state must not move: a proposal is not created, a pending
// proposal is not confirmed, a change set is not closed, a note is not stored.
// (Each action is first shown to work without the fault, so the assertions
// are not passing for a request that was wrong anyway.)
func TestInvariant_8_phase4_state_changes_commit_with_their_audit(t *testing.T) {
	r := newLandingRig(t)
	side := openSideDB(t, r.a.cfg.dataDir)
	base, task := r.base, string(r.task.ID)
	post := func(path string, body any) (int, []byte) {
		t.Helper()
		return apiDo(t, http.MethodPost, base+path, body, appClient)
	}

	// 1. A merge-local proposal is created with its change set and audit.
	side.faultAudit(true)
	auditBefore, csBefore := side.count("audit_log"), side.count("change_sets")
	propBefore := side.count("proposals")
	if code, _ := post("/api/worktrees/wt_p4/merge-local", map[string]string{}); code < 300 {
		t.Fatalf("merge-local succeeded (%d) although its audit entry could not be written", code)
	}
	if got := side.count("proposals"); got != propBefore {
		t.Errorf("proposals: %d -> %d; a proposal was stored without its audit entry", propBefore, got)
	}
	if got := side.count("change_sets"); got != csBefore {
		t.Errorf("change sets: %d -> %d; a change set was stored without its audit entry", csBefore, got)
	}
	if got := side.count("audit_log"); got != auditBefore {
		t.Errorf("audit rows: %d -> %d under the fault", auditBefore, got)
	}
	side.faultAudit(false)
	code, data := post("/api/worktrees/wt_p4/merge-local", map[string]string{})
	if code != 201 {
		t.Fatalf("merge-local without the fault = %d %s", code, data)
	}
	var p api.ProposalDTO
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	var cs api.ChangeSetDTO
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if getJSON(t, base+"/api/tasks/"+task+"/changeset", &cs) == 200 && len(cs.Members) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no change set: %+v", cs)
		}
	}

	// 2. A note is stored with its audit entry.
	var diff api.WorktreeDiffDTO
	if getJSON(t, base+"/api/tasks/"+task+"/repos/"+string(r.repo)+"/diff", &diff) != 200 || len(diff.Files) == 0 {
		t.Fatalf("diff: %+v", diff)
	}
	note := api.CreateNoteRequest{RepoID: r.repo, Path: diff.Files[0].Path, Line: 1, Side: "new", Text: "rename this", DiffHash: diff.Files[0].Hash}
	side.faultAudit(true)
	notesBefore := side.count("review_notes")
	if code, _ := post("/api/tasks/"+task+"/notes", note); code < 300 {
		t.Errorf("note creation succeeded (%d) although its audit entry could not be written", code)
	}
	if got := side.count("review_notes"); got != notesBefore {
		t.Errorf("notes: %d -> %d; a note was stored without its audit entry", notesBefore, got)
	}
	side.faultAudit(false)
	if code, data := post("/api/tasks/"+task+"/notes", note); code >= 300 {
		t.Fatalf("note creation without the fault = %d %s (the fault test above proved nothing)", code, data)
	}

	// 3. Confirming and closing: nothing moves under the fault.
	side.faultAudit(true)
	if code, _ := post("/api/proposals/"+string(p.ID)+"/confirm", api.ConfirmProposalRequest{Hash: p.ActionHash}); code < 300 {
		t.Errorf("confirm succeeded (%d) under the audit fault", code)
	}
	var after api.ProposalDTO
	getJSON(t, base+"/api/proposals/"+string(p.ID), &after)
	if after.Status != core.ProposalPending {
		t.Errorf("proposal is %q after a confirm whose audit entry failed, want pending", after.Status)
	}
	if got := searchtest.GitOutput(t, r.env, r.repoDir, "", "rev-parse", "HEAD"); got != r.mainTip {
		t.Error("the main checkout moved although the confirmation could not be audited")
	}
	// Close needs the proposals decided: reject the pending one without the fault.
	side.faultAudit(false)
	if code, data := post("/api/proposals/"+string(p.ID)+"/reject", map[string]string{}); code >= 300 {
		t.Fatalf("reject = %d %s", code, data)
	}
	side.faultAudit(true)
	if code, _ := post("/api/changesets/"+string(cs.ID)+"/close", nil); code < 300 {
		t.Errorf("close succeeded (%d) under the audit fault", code)
	}
	var csAfter api.ChangeSetDTO
	getJSON(t, base+"/api/tasks/"+task+"/changeset", &csAfter)
	if csAfter.State != api.ChangeSetStateOpen {
		t.Errorf("change set is %q after a close whose audit entry failed, want open", csAfter.State)
	}
	side.faultAudit(false)
	// Control: without the fault the same close works, so the refusal above
	// came from the audit failure and not from a bad request.
	if code, data := post("/api/changesets/"+string(cs.ID)+"/close", nil); code >= 300 {
		t.Errorf("close without the fault = %d %s", code, data)
	}
}
