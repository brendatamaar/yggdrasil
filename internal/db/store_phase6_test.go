package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/core/notify"
)

func seedTask(t *testing.T, s *Store) (core.Project, core.Repo, core.Task) {
	t.Helper()
	p, r := seed(t, s)
	task, err := s.CreateTask(context.Background(), newTask(p, r, nil))
	if err != nil {
		t.Fatal(err)
	}
	return p, r, task
}

func TestNoticesAppendReplayTrimAndGap(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, _, task := seedTask(t, s)
	mk := func(i int64, st notify.Status, src notify.Source) notify.Notice {
		return notify.Notice{ID: core.NoticeID("ntc_" + string(rune('a'+i%26)) + string(rune('a'+i/26%26))), Task: task.ID, TaskTitle: task.Title,
			Status: st, Source: src, SourceRevision: i, CreatedAt: at0.Add(time.Duration(i) * time.Second)}
	}
	var first notify.Notice
	for i := int64(1); i <= int64(notify.MaxRecent)+5; i++ {
		var got notify.Notice
		err := s.Commit(ctx, func(tx *Tx) (err error) {
			got, err = tx.AppendNotice(ctx, mk(i, notify.Done, notify.Source{Kind: notify.SourceTask}))
			return err
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if i == 1 {
			first = got
		}
		if got.Cursor == 0 {
			t.Fatal("the notice has no cursor")
		}
	}
	// Retention keeps the newest 200.
	page, err := s.ListNotices(ctx, 0, 200)
	if err != nil || len(page.Notices) != notify.MaxRecent || page.Gap {
		t.Fatalf("recent page = %d notices, gap %v, %v", len(page.Notices), page.Gap, err)
	}
	if page.Notices[0].Cursor != page.Newest || page.Oldest != page.Newest-int64(notify.MaxRecent)+1 {
		t.Errorf("cursors: newest first %d, oldest %d, newest %d", page.Notices[0].Cursor, page.Oldest, page.Newest)
	}
	// A client that last saw the very first cursor missed trimmed notices: a gap.
	gap, err := s.ListNotices(ctx, first.Cursor, 50)
	if err != nil || !gap.Gap {
		t.Fatalf("after the first cursor: gap %v, %v", gap.Gap, err)
	}
	// A client that is up to date sees no gap and nothing new.
	cur, err := s.ListNotices(ctx, page.Newest, 50)
	if err != nil || cur.Gap || len(cur.Notices) != 0 {
		t.Fatalf("up to date: %+v, %v", cur, err)
	}
	// A replay of a trimmed old transition is a conflict and writes nothing; the
	// high-water mark, not the retained history, is what refuses it.
	err = s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.AppendNotice(ctx, mk(1, notify.Done, notify.Source{Kind: notify.SourceTask}))
		return err
	})
	if err == nil {
		t.Error("a trimmed old transition was accepted again")
	}
	hw, err := s.GetNoticeHighWater(ctx)
	if err != nil || hw[notify.Key{Task: task.ID, Source: notify.Source{Kind: notify.SourceTask}}] != int64(notify.MaxRecent)+5 {
		t.Fatalf("high water = %v, %v", hw, err)
	}
	// The mark never moves back.
	var moved bool
	if err := s.Commit(ctx, func(tx *Tx) (err error) {
		moved, err = tx.AdvanceNoticeHighWater(ctx, notify.Key{Task: task.ID, Source: notify.Source{Kind: notify.SourceTask}}, 3, at0)
		return err
	}); err != nil || moved {
		t.Fatalf("moving the mark back: moved %v, %v", moved, err)
	}
	hw, _ = s.GetNoticeHighWater(ctx)
	if hw[notify.Key{Task: task.ID, Source: notify.Source{Kind: notify.SourceTask}}] != int64(notify.MaxRecent)+5 {
		t.Error("the high-water mark moved back")
	}
	// A verification source keeps its own run sequence.
	vsrc := notify.Source{Kind: notify.SourceVerification, RunID: "run1"}
	if err := s.Commit(ctx, func(tx *Tx) error {
		n := mk(1, notify.VerificationFailed, vsrc)
		n.ID = "ntc_verify"
		_, err := tx.AppendNotice(ctx, n)
		return err
	}); err != nil {
		t.Errorf("a verification notice of another source was refused: %v", err)
	}
	// There is no column for a body: only title, status and metadata exist.
	if n := count(t, openTestDB(t, s), `SELECT count(*) FROM pragma_table_info('notices') WHERE name IN ('body','text','prompt','path','detail','log','diff')`); n != 0 {
		t.Error("the notices table has a free-text column")
	}
}

func openTestDB(t *testing.T, s *Store) *DB { t.Helper(); return s.db }

func TestEffectsRecordIntentBeforeEffect(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r, task := seedTask(t, s)
	_ = p
	var prop core.ProposalID = "prp_1"
	if _, err := s.db.write.ExecContext(ctx, `INSERT INTO proposals (id, task_id, repo_id, kind, status, summary, created_at) VALUES ('prp_1', ?, ?, 'merge_local', 'pending', 's', `+ts+`)`, string(task.ID), string(r.ID)); err != nil {
		t.Fatal(err)
	}
	e := EffectRecord{Proposal: prop, Stage: "create_remote", Attempt: 1, Marker: strings.Repeat("c", 16), CreatedAt: at0, Operation: "op_1"}
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.InsertEffect(ctx, e) }); err != nil {
		t.Fatal(err)
	}
	// The same attempt of the same stage cannot start twice.
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.InsertEffect(ctx, e) }); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate effect: %v, want ErrConflict", err)
	}
	open, err := s.ListOpenEffects(ctx)
	if err != nil || len(open) != 1 || open[0].State != EffectIntent {
		t.Fatalf("open effects = %+v, %v", open, err)
	}
	id := open[0].ID
	if id != EffectID(prop, "create_remote", 1) {
		t.Errorf("effect id %q", id)
	}
	// Succeeded needs a receipt; a stale version conflicts.
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SettleEffect(ctx, id, EffectSucceeded, "", nil, "", 0, at0)
		return err
	}); err == nil {
		t.Error("a succeeded effect without a receipt was accepted")
	}
	kind, data, err := EncodeReceipt(newrepos.RemoteReceipt{RemoteID: "R_1", URL: "https://github.com/acme/x.git"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SettleEffect(ctx, id, EffectSucceeded, kind, json.RawMessage(data), "R_1", 0, at0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SettleEffect(ctx, id, EffectFailed, "", nil, "", 0, at0)
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Errorf("settling at a stale version: %v, want ErrConflict", err)
	}
	got, err := s.GetEffect(ctx, id)
	if err != nil || got.State != EffectSucceeded || got.RemoteID != "R_1" || got.Version != 1 {
		t.Fatalf("effect = %+v, %v", got, err)
	}
	if open, _ := s.ListOpenEffects(ctx); len(open) != 0 {
		t.Error("a settled effect is still open")
	}
}

func TestReceiptsRoundTripAndRefuseCredentials(t *testing.T) {
	for _, r := range []newrepos.Receipt{
		newrepos.DirReceipt{Identity: "vol:1:2"}, newrepos.GitReceipt{GitDirIdentity: "vol:1:3"},
		newrepos.FileReceipt{Name: "README.md", ContentHash: strings.Repeat("a", 64)},
		newrepos.CommitReceipt{Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40), MessageHash: strings.Repeat("d", 64), Author: "A <a@acme.dev>"},
		newrepos.TrackedReceipt{}, newrepos.RemoteReceipt{RemoteID: "R_1", URL: "https://github.com/acme/x.git"},
		newrepos.OriginReceipt{URL: "https://github.com/acme/x.git"}, newrepos.PushReceipt{Head: strings.Repeat("e", 40)},
	} {
		_, data, err := EncodeReceipt(r)
		if err != nil {
			t.Fatalf("%T: %v", r, err)
		}
		back, err := DecodeReceipt(data)
		if err != nil || back != r {
			t.Errorf("%T round trip = %#v, %v", r, back, err)
		}
	}
	if _, _, err := EncodeReceipt(newrepos.OriginReceipt{URL: "https://user:tok@github.com/a/b.git"}); err == nil {
		t.Error("a credential URL was encoded")
	}
	for _, bad := range []string{`{"kind":"nope"}`, `{"kind":"dir","extra":1}`, `{"kind":"origin","url":"https://u:p@h/x"}`, `{`} {
		if _, err := DecodeReceipt(bad); err == nil {
			t.Errorf("%s was decoded", bad)
		}
	}
}

func TestNewRepoStateRoundTripAndRejects(t *testing.T) {
	spec := newReposSpec("prj_1", "rep_1", "shop-new")
	pub := newrepos.PublishSpec{Repo: "rep_1", HostInstance: "github.com", Owner: "acme", OwnerKind: newrepos.OwnerUser, Name: "x",
		Visibility: newrepos.VisibilityPrivate, Branch: "main", ExpectedHead: strings.Repeat("a", 40), Marker: strings.Repeat("b", 16)}
	states := []newrepos.State{
		newrepos.Reserved{Spec: spec, Hash: spec.Hash()},
		newrepos.AwaitingConfirm{Spec: spec, Proposal: "prp_1", Hash: spec.Hash()},
		newrepos.Initializing{Spec: spec, Attempt: 2, Stage: newrepos.StageWriteREADME, Done: []newrepos.Receipt{newrepos.DirReceipt{Identity: "i"}, newrepos.GitReceipt{GitDirIdentity: "g"}}},
		newrepos.Ready{Repo: "rep_1", InitialHead: strings.Repeat("c", 40)},
		newrepos.Publishing{Spec: pub, Attempt: 1, Stage: newrepos.StageAddOrigin, Done: []newrepos.Receipt{newrepos.RemoteReceipt{RemoteID: "R", URL: "https://github.com/acme/x.git"}}},
		newrepos.Published{RemoteID: "R", URL: "https://github.com/acme/x.git", Head: strings.Repeat("c", 40)},
		newrepos.Uncertain{Attempt: 1, Stage: newrepos.StageCreateRemote, Reason: "timed out", Resume: newrepos.ResumePublish{Spec: pub}},
		newrepos.Stopped{Stage: newrepos.StageFirstCommit, Reason: newrepos.StopMissingAuthor, Resume: newrepos.ResumeInit{Spec: spec, Done: []newrepos.Receipt{newrepos.DirReceipt{Identity: "i"}}}},
		newrepos.Stopped{Reason: newrepos.StopRejected, Resume: newrepos.ResumeNone{}},
	}
	for _, st := range states {
		code, stage, attempt, detail, err := encodeNewRepoState(st)
		if err != nil {
			t.Fatalf("%T: %v", st, err)
		}
		back, err := decodeNewRepoState(spec, code, stage, attempt, detail)
		if err != nil {
			t.Fatalf("%T: decode: %v", st, err)
		}
		c2, s2, a2, d2, err := encodeNewRepoState(back)
		if err != nil || c2 != code || s2 != stage || a2 != attempt || d2 != detail {
			t.Errorf("%T: re-encode differs: %v %q %q %d", st, err, c2, s2, a2)
		}
	}
	// Impossible combinations are rejected.
	bad := []struct {
		name, code, stage string
		attempt           int
		detail            string
	}{
		{"stage on reserved", "reserved", "init_git", 0, "{}"},
		{"attempt on ready", "ready", "", 1, `{"initial_head":"x"}`},
		{"initializing without stage", "initializing", "", 1, "{}"},
		{"initializing with publish stage", "initializing", "push_head", 1, "{}"},
		{"initializing attempt 0", "initializing", "init_git", 0, "{}"},
		{"publishing without spec", "publishing", "push_head", 1, "{}"},
		{"ready without head", "ready", "", 0, "{}"},
		{"published with credential URL", "published", "", 0, `{"remote_id":"R","head":"h","url":"https://u:p@h/x"}`},
		{"stopped without reason", "stopped", "", 0, `{"resume":{"kind":"none"}}`},
		{"stopped without resume", "stopped", "", 0, `{"reason":"rejected"}`},
		{"uncertain without stage", "uncertain", "", 1, `{"resume":{"kind":"none"}}`},
		{"awaiting without proposal", "awaiting_confirm", "", 0, "{}"},
		{"unknown state", "nope", "", 0, "{}"},
		{"unknown detail field", "reserved", "", 0, `{"token":"x"}`},
	}
	for _, c := range bad {
		if _, err := decodeNewRepoState(spec, c.code, c.stage, c.attempt, c.detail); err == nil {
			t.Errorf("%s was accepted", c.name)
		}
	}
}

func TestSaveReservationFencedByVersion(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, _ := seed(t, s)
	spec := newReposSpec(p.ID, "rep_n", "shop-new")
	if err := s.Commit(ctx, func(tx *Tx) error {
		return tx.ReserveRepo(ctx, ReserveRepo{Operation: "op_1", Project: p.ID, Repo: "rep_n", SetupTask: "tsk_s",
			Path: "/work/acme/shop-new", Spec: spec, Agent: "mock", Title: "Set up shop-new", Now: at0})
	}); err != nil {
		t.Fatal(err)
	}
	next := newrepos.AwaitingConfirm{Spec: spec, Proposal: "prp_9", Hash: spec.Hash()}
	// The proposal must exist for the foreign key.
	if _, err := s.db.write.ExecContext(ctx, `INSERT INTO proposals (id, kind, status, summary, created_at) VALUES ('prp_9', 'init_repo', 'pending', 's', `+ts+`)`); err != nil {
		t.Fatal(err)
	}
	var v int64
	if err := s.Commit(ctx, func(tx *Tx) (err error) {
		v, err = tx.SaveReservation(ctx, "op_1", next, "prp_9", 0, at0)
		return err
	}); err != nil || v != 1 {
		t.Fatalf("save = %d, %v", v, err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveReservation(ctx, "op_1", next, "prp_9", 0, at0)
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Errorf("stale save: %v, want ErrConflict", err)
	}
	rec, err := s.GetReservation(ctx, "op_1")
	if err != nil || rec.Operation.Version != 1 || rec.Proposal != "prp_9" {
		t.Fatalf("reservation = %+v, %v", rec, err)
	}
	if _, ok := rec.Operation.State.(newrepos.AwaitingConfirm); !ok {
		t.Errorf("state = %T", rec.Operation.State)
	}
	if un, _ := s.ListUnsettledReservations(ctx); len(un) != 0 {
		t.Error("an awaiting operation is listed as unsettled")
	}
}

func TestActionRequestsAreIdempotentAndDigestBound(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, r, task := seedTask(t, s)
	req := actions.Request{RequestID: "req_1", RepoID: r.ID, Kind: actions.DiagnoseRepo, Agent: "mock"}
	rec := ActionRecord{Request: req, Digest: req.Digest(), State: ActionSubmitting, Attempt: 1, Key: "action:req_1:1", CreatedAt: at0}
	var created bool
	if err := s.Commit(ctx, func(tx *Tx) (err error) { _, created, err = tx.BeginActionRequest(ctx, rec); return err }); err != nil || !created {
		t.Fatalf("begin = %v, %v", created, err)
	}
	// The same request again finds the first and creates nothing.
	var again ActionRecord
	if err := s.Commit(ctx, func(tx *Tx) (err error) { again, created, err = tx.BeginActionRequest(ctx, rec); return err }); err != nil || created {
		t.Fatalf("repeat = %v, %v", created, err)
	}
	if again.State != ActionSubmitting || again.Key != "action:req_1:1" {
		t.Errorf("repeat returned %+v", again)
	}
	// The same ID with another request is refused.
	other := req
	other.Kind = actions.DiagnoseBuild
	other.SourceID = "b1"
	orec := ActionRecord{Request: other, Digest: other.Digest(), State: ActionSubmitting, Attempt: 1, Key: "k", CreatedAt: at0}
	if err := s.Commit(ctx, func(tx *Tx) error { _, _, err := tx.BeginActionRequest(ctx, orec); return err }); !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("reused id: %v, want ErrDigestMismatch", err)
	}
	// A digest that does not match the request is refused before any write.
	bad := rec
	bad.Request.RequestID = "req_2"
	bad.Digest = strings.Repeat("0", 64)
	if err := s.Commit(ctx, func(tx *Tx) error { _, _, err := tx.BeginActionRequest(ctx, bad); return err }); err == nil {
		t.Error("a forged digest was accepted")
	}
	if un, err := s.ListUnsettledActionRequests(ctx); err != nil || len(un) != 1 {
		t.Fatalf("unsettled = %d, %v", len(un), err)
	}
	// The result is stored with its links in one commit; a stale version conflicts.
	done := rec
	done.State, done.Task = ActionCompleted, task.ID
	if err := s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveActionRequest(ctx, done, 0, at0); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveActionRequest(ctx, done, 0, at0); return err }); !errors.Is(err, ErrConflict) {
		t.Errorf("stale save: %v, want ErrConflict", err)
	}
	got, err := s.GetActionRequest(ctx, "req_1")
	if err != nil || got.State != ActionCompleted || got.Task != task.ID || got.Version != 1 {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	if un, _ := s.ListUnsettledActionRequests(ctx); len(un) != 0 {
		t.Error("a completed request is unsettled")
	}
	// A refused request needs a reason.
	ref := ActionRecord{Request: actions.Request{RequestID: "req_3", RepoID: r.ID, Kind: actions.DiagnoseRepo, Agent: "mock"}, State: ActionRefused, CreatedAt: at0}
	ref.Digest = ref.Request.Digest()
	if err := s.Commit(ctx, func(tx *Tx) error { _, _, err := tx.BeginActionRequest(ctx, ref); return err }); err == nil {
		t.Error("a refused request without a reason was stored")
	}
}

func TestOutboxClaimLeaseAndReconcileMarker(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	row := OutboxRow{Kind: OutboxActionRequest, Subject: "req_1", Attempt: 1, Stage: "", Payload: json.RawMessage(`{"k":1}`)}
	var inserted bool
	if err := s.Commit(ctx, func(tx *Tx) (err error) { inserted, err = tx.EnqueueOutbox(ctx, row, at0); return err }); err != nil || !inserted {
		t.Fatalf("enqueue = %v, %v", inserted, err)
	}
	// The deterministic ID makes a second enqueue of the same intent a no-op.
	if err := s.Commit(ctx, func(tx *Tx) (err error) { inserted, err = tx.EnqueueOutbox(ctx, row, at0); return err }); err != nil || inserted {
		t.Fatalf("second enqueue = %v, %v", inserted, err)
	}
	var got []OutboxRow
	claim := func(worker string, now time.Time) []OutboxRow {
		var out []OutboxRow
		if err := s.Commit(ctx, func(tx *Tx) (err error) { out, err = tx.ClaimOutbox(ctx, worker, now, time.Minute, 5); return err }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got = claim("w1", at0)
	if len(got) != 1 || got[0].ClaimCount != 1 || got[0].ID != OutboxID("req_1", 1, "") {
		t.Fatalf("first claim = %+v", got)
	}
	// While the lease holds, nobody else gets the row.
	if other := claim("w2", at0.Add(10*time.Second)); len(other) != 0 {
		t.Errorf("a leased row was claimed by another worker: %+v", other)
	}
	// After the lease expires it is claimed again, with ClaimCount > 1: reconcile first.
	again := claim("w2", at0.Add(2*time.Minute))
	if len(again) != 1 || again[0].ClaimCount != 2 {
		t.Fatalf("reclaim = %+v", again)
	}
	// Only the current claimant can complete it.
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.CompleteOutbox(ctx, again[0].ID, "w1", at0) }); !errors.Is(err, ErrNotFound) {
		t.Errorf("complete by the old claimant: %v, want ErrNotFound", err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error { return tx.CompleteOutbox(ctx, again[0].ID, "w2", at0.Add(3*time.Minute)) }); err != nil {
		t.Fatal(err)
	}
	if rest := claim("w3", at0.Add(10*time.Minute)); len(rest) != 0 {
		t.Errorf("a done row was claimed: %+v", rest)
	}
}

func TestIssueDraftDedupAtTheDatabase(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, r := seed(t, s)
	mk := func(title string) core.Task {
		tk := newTask(p, r, nil)
		tk.Title = title
		tk.Status = core.TaskDraft
		tk.Source = &core.TaskSource{Kind: core.SourceIssue, Instance: "git.acme.dev", Project: "acme/shop-api", Number: 12,
			DedupKey: core.IssueDedupKey("git.acme.dev", "acme/shop-api", 12)}
		return tk
	}
	first, err := s.CreateTask(ctx, mk("Fix login"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, mk("Fix login again")); !errors.Is(err, ErrConflict) {
		t.Errorf("the second import of the same issue: %v, want ErrConflict", err)
	}
	got, err := s.GetDraftByDedupKey(ctx, p.ID, core.IssueDedupKey("git.acme.dev", "acme/shop-api", 12))
	if err != nil || got.ID != first.ID || got.Source == nil || got.Source.Number != 12 || got.Status != core.TaskDraft {
		t.Fatalf("draft = %+v, %v", got, err)
	}
	if _, err := s.GetDraftByDedupKey(ctx, p.ID, "issue:other#1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key: %v", err)
	}
}
