package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
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

// auditRows snapshots complete entries for one subject, independently of the
// API projection and of unrelated background audit activity.
func (s *sideDB) auditRows(subject string) []core.AuditEntry {
	s.t.Helper()
	rows, err := s.db.Query(`SELECT id, at, actor, action, subject, detail FROM audit_log WHERE subject = ? ORDER BY at, id`, subject)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := []core.AuditEntry{}
	for rows.Next() {
		var e core.AuditEntry
		var at, detail string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Subject, &detail); err != nil {
			s.t.Fatal(err)
		}
		if e.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			s.t.Fatal(err)
		}
		e.Detail = []byte(detail)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		s.t.Fatal(err)
	}
	return out
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

	// 3. Confirming: nothing moves under the fault.
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
	side.faultAudit(false)

	// 4. Close an independent empty Open set. Rejecting the sole proposal
	// above would let MembersChanged legitimately auto-close its set before
	// fault injection (all rejected => Closed). Empty membership stays Open
	// under reconciliation and satisfies Open.Close's no-active-member guard.
	ctx := context.Background()
	closeTask := dbtest.Task(t, r.a.store, core.Task{ProjectID: r.task.ProjectID, Scope: core.ScopeRepo,
		RepoIDs: []core.RepoID{r.repo}, Mode: core.ModeCode, Status: core.TaskReview})
	setID := core.ChangeSetID(db.NewID("cs"))
	now := time.Now().UTC()
	if err := r.a.store.Commit(ctx, func(tx *db.Tx) error {
		if _, err := tx.CreateChangeSet(ctx, core.ChangeSet{ID: setID, TaskID: closeTask.ID,
			Status: core.ChangeSetOpen, MergeOrder: []core.RepoID{r.repo}, CreatedAt: now}); err != nil {
			return err
		}
		entry, err := audit.Prepare(core.AuditEntry{Actor: "test", Action: "changeset.create",
			Subject: "changeset:" + string(setID), Detail: []byte(`{}`)}, safety.NewRedactor(), core.AuditID(db.NewID("aud")), now)
		if err != nil {
			return err
		}
		return tx.AppendAudit(ctx, entry)
	}); err != nil {
		t.Fatal(err)
	}
	readSet := func() api.ChangeSetDTO {
		t.Helper()
		var got api.ChangeSetDTO
		if code := getJSON(t, base+"/api/tasks/"+string(closeTask.ID)+"/changeset", &got); code != http.StatusOK {
			t.Fatalf("close fixture GET = %d", code)
		}
		if got.ID != setID || got.TaskID != closeTask.ID || got.ID == cs.ID ||
			len(got.Members) != 0 || !reflect.DeepEqual(got.Order, []core.RepoID{r.repo}) ||
			len(got.Repos) != 1 || got.Repos[0].RepoID != r.repo || got.Run.State != api.RunStateIdle || got.Confirming != nil {
			t.Fatalf("close fixture identity/membership: %+v", got)
		}
		return got
	}
	before := readSet()
	if before.State != api.ChangeSetStateOpen || before.Status != core.ChangeSetOpen {
		t.Fatalf("close precondition: %+v", before)
	}
	rowBefore, err := r.a.store.GetChangeSetRow(ctx, setID)
	if err != nil {
		t.Fatal(err)
	}
	subject := "changeset:" + string(setID)
	entriesBefore := side.auditRows(subject)
	if len(entriesBefore) != 1 || entriesBefore[0].Action != "changeset.create" {
		t.Fatalf("close fixture audit: %+v", entriesBefore)
	}
	closePath := "/api/changesets/" + string(setID) + "/close"
	side.faultAudit(true)
	if code, data := post(closePath, nil); code != http.StatusInternalServerError {
		t.Fatalf("close under audit fault = %d %s, want 500", code, data)
	}
	rowAfter, err := r.a.store.GetChangeSetRow(ctx, setID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rowAfter, rowBefore) {
		t.Errorf("close changed stored state under audit fault: before %+v, after %+v", rowBefore, rowAfter)
	}
	if got := readSet(); !reflect.DeepEqual(got, before) {
		t.Errorf("close changed HTTP state under audit fault: before %+v, after %+v", before, got)
	}
	if got := side.auditRows(subject); !reflect.DeepEqual(got, entriesBefore) {
		t.Errorf("close changed audit under fault: before %+v, after %+v", entriesBefore, got)
	}
	side.faultAudit(false)
	// The identical request must now commit the transition AND its audit.
	if code, data := post(closePath, nil); code != http.StatusOK {
		t.Fatalf("close without audit fault = %d %s, want 200", code, data)
	}
	closed := readSet()
	want := before
	want.State, want.Status = api.ChangeSetStateClosed, core.ChangeSetClosed
	if !reflect.DeepEqual(closed, want) {
		t.Errorf("close control: got %+v, want %+v", closed, want)
	}
	rowClosed, err := r.a.store.GetChangeSetRow(ctx, setID)
	if err != nil {
		t.Fatal(err)
	}
	wantRow := rowBefore
	wantRow.Status, wantRow.Version = core.ChangeSetClosed, rowBefore.Version+1
	if !reflect.DeepEqual(rowClosed, wantRow) {
		t.Errorf("close control stored state: got %+v, want %+v", rowClosed, wantRow)
	}
	entriesClosed := side.auditRows(subject)
	if len(entriesClosed) != len(entriesBefore)+1 {
		t.Fatalf("close control audit rows = %d, want %d", len(entriesClosed), len(entriesBefore)+1)
	}
	var closeEntry core.AuditEntry
	var unchanged []core.AuditEntry
	for _, e := range entriesClosed {
		if e.Action == "changeset.close" {
			closeEntry = e
		} else {
			unchanged = append(unchanged, e)
		}
	}
	var detail map[string]string
	if err := json.Unmarshal(closeEntry.Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unchanged, entriesBefore) || closeEntry.ID == "" || closeEntry.At.IsZero() ||
		closeEntry.Actor != "user" || closeEntry.Subject != subject ||
		!reflect.DeepEqual(detail, map[string]string{"task": string(closeTask.ID), "outcome": "ok"}) {
		t.Errorf("close control audit: %+v, detail %+v", entriesClosed, detail)
	}
}
