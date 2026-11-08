package changesets_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/changesets/changesetstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func baseOpen() cs.Open {
	return cs.Open{
		Set:     cs.Header{ID: "cs_1", Task: "tsk_1", Repos: []core.RepoID{rep1, rep2}, Order: []core.RepoID{rep2, rep1}},
		Members: []cs.Member{{ID: "prp_1", Repo: rep1, Kind: core.ProposalCommit, Status: core.ProposalPending, Hash: changesetstest.Hash(1)}},
		LastRun: cs.RunIdle{},
	}
}

func TestRunResultRoundTrip(t *testing.T) {
	o := baseOpen()
	states := []cs.State{
		o,
		withRun(o, cs.RunCompleted{}),
		withRun(o, cs.RunHalted{At: "prp_1", Reason: "a proposal failed"}),
		cs.Confirming{Open: o, Scope: cs.ScopeAll{}, Current: "prp_1", Queue: []cs.Planned{{ID: "prp_2", Hash: changesetstest.Hash(2)}}},
		cs.Confirming{Open: o, Scope: cs.ScopeRepo{Repo: rep1}, Current: "prp_1"},
		cs.Shipped{Open: withRun(o, cs.RunCompleted{})},
	}
	for i, st := range states {
		raw, err := changesets.FormatRun(st)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		last, run, err := changesets.ParseRun(raw)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if c, ok := st.(cs.Confirming); ok {
			if run == nil || !reflect.DeepEqual(run.Scope, c.Scope) || run.Current != c.Current || !reflect.DeepEqual(run.Queue, c.Queue) {
				t.Fatalf("%d: confirming did not round trip: %+v", i, run)
			}
			continue
		}
		if run != nil || !reflect.DeepEqual(last, openOfTest(st).LastRun) {
			t.Fatalf("%d: last run %#v", i, last)
		}
	}
}

func withRun(o cs.Open, r cs.RunResult) cs.Open { o.LastRun = r; return o }

func openOfTest(st cs.State) cs.Open { return openOf(st) }

func TestParseRunRejectsEachRule(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":            ``,
		"unknown kind":     `{"kind":"paused"}`,
		"unknown field":    `{"kind":"idle","x":1}`,
		"trailing data":    `{"kind":"idle"} {}`,
		"halted no id":     `{"kind":"halted"}`,
		"unknown scope":    `{"kind":"confirming","scope":"some","current":"prp_1"}`,
		"repo scope no id": `{"kind":"confirming","scope":"repo","current":"prp_1"}`,
		"no current":       `{"kind":"confirming","scope":"all"}`,
		"bad queue hash":   `{"kind":"confirming","scope":"all","current":"prp_1","queue":[{"id":"prp_2","hash":"zz"}]}`,
		"bad queue id":     `{"kind":"confirming","scope":"all","current":"prp_1","queue":[{"id":"","hash":"` + changesetstest.Hash(1) + `"}]}`,
	} {
		if _, _, err := changesets.ParseRun([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func rows(status core.ChangeSetStatus, order []core.RepoID, run string, members ...db.ChangeSetMemberRow) (db.ChangeSetRow, []db.ChangeSetMemberRow) {
	return db.ChangeSetRow{ID: "cs_1", Task: "tsk_1", Status: status, MergeOrder: order, RunResult: json.RawMessage(run)}, members
}

func member(id string, repo core.RepoID, kind core.ProposalKind, st core.ProposalStatus, n int) db.ChangeSetMemberRow {
	return db.ChangeSetMemberRow{ID: core.ProposalID(id), Repo: repo, Kind: kind, Status: st, Hash: changesetstest.Hash(n)}
}

func TestStateFromRowsAcceptsValidAndAppendsMissingRepos(t *testing.T) {
	row, ms := rows(core.ChangeSetOpen, nil, `{"kind":"idle"}`, member("prp_1", rep2, core.ProposalCommit, core.ProposalPending, 1))
	st, err := changesets.StateFromRows(row, []core.RepoID{rep1, rep2}, ms)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("accepted but not valid: %v", err)
	}
	if o := st.(cs.Open); !cs.IsPermutation(o.Set.Order, o.Set.Repos) {
		t.Fatalf("order %v", o.Set.Order)
	}
	row.MergeOrder = []core.RepoID{rep2}
	st, err = changesets.StateFromRows(row, []core.RepoID{rep1, rep2}, ms)
	if err != nil || !reflect.DeepEqual(st.(cs.Open).Set.Order, []core.RepoID{rep2, rep1}) {
		t.Fatalf("partial order: %v %v", st, err)
	}
}

func TestStateFromRowsRejectsEachRule(t *testing.T) {
	good := member("prp_1", rep1, core.ProposalCommit, core.ProposalPending, 1)
	cases := map[string]func() (db.ChangeSetRow, []db.ChangeSetMemberRow){
		"order has an unknown repo": func() (db.ChangeSetRow, []db.ChangeSetMemberRow) {
			return rows(core.ChangeSetOpen, []core.RepoID{"rep_9"}, `{"kind":"idle"}`, good)
		},
		"order repeats a repo": func() (db.ChangeSetRow, []db.ChangeSetMemberRow) {
			return rows(core.ChangeSetOpen, []core.RepoID{rep1, rep1}, `{"kind":"idle"}`, good)
		},
		"unknown status": func() (db.ChangeSetRow, []db.ChangeSetMemberRow) {
			return rows("lost", nil, `{"kind":"idle"}`, good)
		},
		"bad member hash": func() (db.ChangeSetRow, []db.ChangeSetMemberRow) {
			m := good
			m.Hash = "abc"
			return rows(core.ChangeSetOpen, nil, `{"kind":"idle"}`, m)
		},
		"unknown member status": func() (db.ChangeSetRow, []db.ChangeSetMemberRow) {
			m := good
			m.Status = "weird"
			return rows(core.ChangeSetOpen, nil, `{"kind":"idle"}`, m)
		},
		"unknown member kind": func() (db.ChangeSetRow, []db.ChangeSetMemberRow) {
			m := good
			m.Kind = "format_disk"
			return rows(core.ChangeSetOpen, nil, `{"kind":"idle"}`, m)
		},
		"bad run result": func() (db.ChangeSetRow, []db.ChangeSetMemberRow) {
			return rows(core.ChangeSetOpen, nil, `{"kind":"x"}`, good)
		},
		"run in a shipped set": func() (db.ChangeSetRow, []db.ChangeSetMemberRow) {
			return rows(core.ChangeSetShipped, nil, `{"kind":"confirming","scope":"all","current":"prp_1"}`, good)
		},
		"member without a repo": func() (db.ChangeSetRow, []db.ChangeSetMemberRow) {
			m := good
			m.Repo = ""
			return rows(core.ChangeSetOpen, nil, `{"kind":"idle"}`, m)
		},
	}
	for name, mk := range cases {
		row, ms := mk()
		if _, err := changesets.StateFromRows(row, []core.RepoID{rep1}, ms); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStateFromRowsLinksOnlyFromExecutedMergeRequests(t *testing.T) {
	mr := member("prp_1", rep1, core.ProposalMergeRequest, core.ProposalExecuted, 1)
	mr.MRURL = "https://git.example.com/a/b/-/merge_requests/1"
	failed := member("prp_2", rep2, core.ProposalMergeRequest, core.ProposalFailed, 2)
	failed.MRURL = "https://git.example.com/a/c/-/merge_requests/2"
	push := member("prp_3", rep2, core.ProposalPush, core.ProposalExecuted, 3)
	push.MRURL = "https://git.example.com/other"
	row, ms := rows(core.ChangeSetOpen, nil, `{"kind":"idle"}`, mr, failed, push)
	st, err := changesets.StateFromRows(row, nil, ms)
	if err != nil {
		t.Fatal(err)
	}
	links := st.(cs.Open).Links
	if len(links) != 1 || links[rep1] != mr.MRURL {
		t.Fatalf("links %v", links)
	}
}

func TestParseOrder(t *testing.T) {
	repos := []core.RepoID{rep1, rep2}
	got, err := changesets.ParseOrder([]string{"rep_2", "rep_1"}, repos)
	if err != nil || !reflect.DeepEqual(got, []core.RepoID{rep2, rep1}) {
		t.Fatalf("%v %v", got, err)
	}
	if !reflect.DeepEqual(changesets.FormatOrder(got), []string{"rep_2", "rep_1"}) {
		t.Fatal("round trip")
	}
	big := make([]string, cs.MaxRepos+1)
	for i := range big {
		big[i] = "rep_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for name, in := range map[string][]string{
		"empty": nil, "duplicate": {"rep_1", "rep_1"}, "unknown": {"rep_1", "rep_9"}, "missing one": {"rep_1"},
		"bad id": {"rep 1", "rep_2"}, "too many": big, "blank": {"", "rep_2"},
	} {
		if _, err := changesets.ParseOrder(in, repos); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDecodeRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	if _, err := changesets.DecodeOrder([]byte(`{"order":["rep_1"]}`)); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{``, `{"order":["rep_1"],"x":1}`, `{"order":["rep_1"]}{}`, `[]`, `{"order":"rep_1"}`, strings.Repeat(" ", 300<<10)} {
		if _, err := changesets.DecodeOrder([]byte(body)); err == nil {
			t.Errorf("order body %.30q accepted", body)
		}
		if _, err := changesets.DecodeConfirmRun([]byte(body)); err == nil && body != `{"order":["rep_1"]}` {
			t.Errorf("confirm body %.30q accepted", body)
		}
	}
	if _, err := changesets.DecodeNote([]byte(`{"repo":"rep_1","path":"a","line":1,"side":"new","text":"x","diffHash":"h","id":"nte_evil"}`)); err == nil {
		t.Error("a client-chosen note ID was accepted")
	}
}

func TestParseConfirmRun(t *testing.T) {
	repos := []core.RepoID{rep1, rep2}
	ms := []cs.Member{{ID: "prp_1"}, {ID: "prp_2"}}
	h := changesetstest.Hash(1)
	ok := changesets.RawConfirm{Scope: "repo", Repo: "rep_1", Seen: map[string]string{"prp_1": h}, Ack: []string{"dependent_before_dependency"}}
	scope, seen, ack, err := changesets.ParseConfirmRun(ok, repos, ms)
	if err != nil || scope != (cs.ScopeRepo{Repo: rep1}) || seen["prp_1"] != h || len(ack) != 1 {
		t.Fatalf("%v %v %v %v", scope, seen, ack, err)
	}
	mut := func(f func(*changesets.RawConfirm)) changesets.RawConfirm {
		c := ok
		c.Seen = map[string]string{"prp_1": h}
		f(&c)
		return c
	}
	for name, c := range map[string]changesets.RawConfirm{
		"unknown scope":       mut(func(c *changesets.RawConfirm) { c.Scope = "some" }),
		"all with a repo":     mut(func(c *changesets.RawConfirm) { c.Scope = "all" }),
		"repo not in the set": mut(func(c *changesets.RawConfirm) { c.Repo = "rep_9" }),
		"repo scope no repo":  mut(func(c *changesets.RawConfirm) { c.Repo = "" }),
		"no seen":             mut(func(c *changesets.RawConfirm) { c.Seen = nil }),
		"short hash":          mut(func(c *changesets.RawConfirm) { c.Seen["prp_1"] = "abc" }),
		"upper-case hash":     mut(func(c *changesets.RawConfirm) { c.Seen["prp_1"] = strings.ToUpper(strings.Repeat("a", 64)) }),
		"proposal not in set": mut(func(c *changesets.RawConfirm) { c.Seen["prp_9"] = h }),
		"unknown warning":     mut(func(c *changesets.RawConfirm) { c.Ack = []string{"nope"} }),
	} {
		if _, _, _, err := changesets.ParseConfirmRun(c, repos, ms); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNoteRowRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	base := review.Note{
		ID: "nte_1", Task: "tsk_1", Repo: rep1, Anchor: review.Anchor{Path: "src/a.go", Line: 4, Side: review.SideOld},
		Text: "why?", DiffHash: changesetstest.Hash(3), Created: at,
	}
	for _, st := range []review.NoteState{review.NoteOpen{}, review.NoteSending{Batch: "bat_1", At: at}, review.NoteSent{Turn: "trn_1", At: at}, review.NoteOutdated{At: at}} {
		n := base
		n.State = st
		got, err := changesets.NoteFromRow(changesets.RowOfNote(n))
		if err != nil || !reflect.DeepEqual(got, n) {
			t.Fatalf("%T: %+v %v", st, got, err)
		}
	}
}

func TestNoteFromRowRejectsEachRule(t *testing.T) {
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	ok := db.NoteRow{ID: "nte_1", Task: "tsk_1", Repo: rep1, Path: "a.go", Line: 1, Side: "new", Text: "x", DiffHash: changesetstest.Hash(1), State: "open", CreatedAt: at}
	if _, err := changesets.NoteFromRow(ok); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func(*db.NoteRow){
		"unknown state":    func(r *db.NoteRow) { r.State = "lost" },
		"sending no time":  func(r *db.NoteRow) { r.State = "sending"; r.Batch = "b" },
		"sending no batch": func(r *db.NoteRow) { r.State = "sending"; r.StateAt = &at },
		"sent no turn":     func(r *db.NoteRow) { r.State = "sent"; r.StateAt = &at },
		"outdated no time": func(r *db.NoteRow) { r.State = "outdated" },
		"path escapes":     func(r *db.NoteRow) { r.Path = "../x" },
		"bad side":         func(r *db.NoteRow) { r.Side = "left" },
		"bad hash":         func(r *db.NoteRow) { r.DiffHash = "x" },
		"line zero":        func(r *db.NoteRow) { r.Line = 0 },
		"empty text":       func(r *db.NoteRow) { r.Text = " " },
		"control in text":  func(r *db.NoteRow) { r.Text = "a\x01" },
	} {
		r := ok
		f(&r)
		if _, err := changesets.NoteFromRow(r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseNoteIDs(t *testing.T) {
	if ids, err := changesets.ParseNoteIDs([]string{"nte_1", "nte_2"}); err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	many := make([]string, review.MaxNotesPerTask+1)
	for i := range many {
		many[i] = "nte_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for name, in := range map[string][]string{"none": nil, "duplicate": {"nte_1", "nte_1"}, "blank": {""}, "space": {"a b"}, "too many": many} {
		if _, err := changesets.ParseNoteIDs(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Fuzz targets: nothing panics, and whatever is accepted is valid.

func FuzzParseRun(f *testing.F) {
	f.Add(`{"kind":"idle"}`)
	f.Add(`{"kind":"confirming","scope":"repo","repo":"rep_1","current":"prp_1","queue":[]}`)
	f.Fuzz(func(t *testing.T, s string) {
		last, run, err := changesets.ParseRun([]byte(s))
		if err != nil {
			return
		}
		if run == nil && last == nil {
			t.Fatal("accepted nothing")
		}
		if run != nil {
			if run.Scope.Validate() != nil || run.Current.Validate() != nil {
				t.Fatalf("accepted but not valid: %+v", run)
			}
			o := baseOpen()
			run.Open = o
			if _, err := changesets.FormatRun(*run); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func FuzzParseOrder(f *testing.F) {
	f.Add("rep_1", "rep_2")
	f.Fuzz(func(t *testing.T, a, b string) {
		repos := []core.RepoID{rep1, rep2}
		got, err := changesets.ParseOrder([]string{a, b}, repos)
		if err == nil && !cs.IsPermutation(got, repos) {
			t.Fatalf("accepted %v", got)
		}
	})
}

func FuzzParseConfirmRun(f *testing.F) {
	f.Add("all", "", "prp_1", changesetstest.Hash(1), "dependent_before_dependency")
	f.Fuzz(func(t *testing.T, scope, repo, id, hash, ack string) {
		repos := []core.RepoID{rep1, rep2}
		ms := []cs.Member{{ID: "prp_1"}}
		sc, seen, acks, err := changesets.ParseConfirmRun(changesets.RawConfirm{Scope: scope, Repo: repo, Seen: map[string]string{id: hash}, Ack: []string{ack}}, repos, ms)
		if err != nil {
			return
		}
		if sc.Validate() != nil || len(seen) != 1 || len(acks) != 1 || acks[0].Validate() != nil {
			t.Fatalf("accepted but not valid: %v %v %v", sc, seen, acks)
		}
		for id, h := range seen {
			if id.Validate() != nil || len(h) != 64 {
				t.Fatalf("seen %v", seen)
			}
		}
	})
}

func FuzzNoteFromRow(f *testing.F) {
	f.Add("open", "a.go", "new", "x", 1)
	f.Fuzz(func(t *testing.T, state, path, side, text string, line int) {
		at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
		r := db.NoteRow{ID: "nte_1", Task: "tsk_1", Repo: rep1, Path: path, Line: line, Side: side, Text: text, DiffHash: changesetstest.Hash(1), State: state, Batch: "b", Turn: "t", StateAt: &at, CreatedAt: at}
		n, err := changesets.NoteFromRow(r)
		if err != nil {
			return
		}
		if n.Validate() != nil {
			t.Fatalf("accepted but not valid: %+v", n)
		}
		if back := changesets.RowOfNote(n); back.Path != r.Path || back.State != r.State {
			t.Fatalf("round trip %+v", back)
		}
	})
}
