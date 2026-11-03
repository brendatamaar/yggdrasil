package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/landing"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
)

// The review, ship and landing steps of TestPhase4EndToEnd.

const (
	p4MoneyPath    = "src/main/java/com/example/acme/core/Money.java"
	p4CheckoutPath = "src/main/java/com/example/acme/checkout/Checkout.java"
	p4NotesPath    = "docs/RELEASE-NOTES.txt"
	p4NotesBase    = "Release notes\n\n0.1.0\n- first storefront build\n"
	p4NotesV1      = "Release notes\r\n\r\n0.1.0\r\n- first storefront build\r\n- currency shown in the cart\r\n"
	p4MoneyV2      = "package com.example.acme.core;\n\npublic record Money(long cents, String currency) {\n    public String label() {\n        return currency + \" \" + cents;\n    }\n\n    public boolean isZero() {\n        return cents == 0;\n    }\n}\n"
)

func (e *p4) diffOf(r *p4Repo) api.WorktreeDiffDTO {
	e.t.Helper()
	var d api.WorktreeDiffDTO
	e.must(http.MethodGet, "/api/tasks/"+string(e.task)+"/repos/"+string(r.id)+"/diff", nil, &d)
	return d
}

func diffFile(d api.WorktreeDiffDTO, path string) (api.WorktreeDiffFileDTO, bool) {
	for _, f := range d.Files {
		if f.Path == path {
			return f, true
		}
	}
	return api.WorktreeDiffFileDTO{}, false
}

// reviewStep: the diff endpoint with its EOL-only flag, and a note that goes
// Outdated when the line changes (RV-01, RV-03, CRLF handling).
func (e *p4) reviewStep(core1, sf *p4Repo, write func(*p4Repo, string, string)) {
	t := e.t
	t.Helper()

	// An edit that only flips the CRLF file to LF is flagged EOL-only.
	write(sf, p4NotesPath, p4NotesBase)
	d := e.diffOf(sf)
	f, ok := diffFile(d, p4NotesPath)
	if !ok || !f.EOLOnly {
		t.Errorf("an LF rewrite of the CRLF notes: file = %+v, found %v; want eolOnly (RV-01 CRLF handling)", f, ok)
	}
	// The agent's real edit keeps CRLF: a content change, not EOL-only.
	write(sf, p4NotesPath, p4NotesV1)
	d = e.diffOf(sf)
	f, ok = diffFile(d, p4NotesPath)
	if !ok || f.EOLOnly || f.Added == 0 || !strings.Contains(d.Text, "currency shown in the cart") {
		t.Errorf("the CRLF edit: file = %+v, found %v; want a content change", f, ok)
	}
	if b, err := os.ReadFile(filepath.Join(sf.wtDir, filepath.FromSlash(p4NotesPath))); err != nil || string(b) != p4NotesV1 {
		t.Errorf("the notes file lost its CRLF endings: %q, %v", b, err)
	}

	// A note on a line of shop-core, anchored to the file's current hash.
	cd := e.diffOf(core1)
	mf, ok := diffFile(cd, p4MoneyPath)
	if !ok || mf.Hash == "" {
		t.Fatalf("no diff for %s: %+v", p4MoneyPath, cd.Files)
	}
	var note api.ReviewNoteDTO
	code, data := e.do(http.MethodPost, "/api/tasks/"+string(e.task)+"/notes", api.CreateNoteRequest{
		RepoID: core1.id, Path: p4MoneyPath, Line: 5, Side: "new", Text: "Should label() live in the formatter?", DiffHash: mf.Hash,
	}, &note)
	if code >= 300 || note.ID == "" || note.State != api.NoteOpen {
		t.Fatalf("add note = %d %s", code, data)
	}
	// The agent edits the file again: the note's hash no longer matches.
	write(core1, p4MoneyPath, p4MoneyV2)
	e.wait("the note to go outdated", 120*time.Second, func() (bool, string) {
		var ns api.ReviewNotesResponse
		e.do(http.MethodGet, "/api/tasks/"+string(e.task)+"/notes", nil, &ns)
		return len(ns.Notes) == 1 && ns.Notes[0].State == api.NoteOutdated, fmt.Sprintf("%+v", ns)
	})
}

var propRE = regexp.MustCompile(`prp_[0-9a-f]+`)

func (e *p4) propose(tool string, args map[string]any) core.ProposalID {
	e.t.Helper()
	raw, isErr := e.mcpCall(tool, args)
	if isErr {
		e.t.Fatalf("%s %v: %s", tool, args, raw)
	}
	id := propRE.FindString(raw)
	if id == "" {
		e.t.Fatalf("%s: no proposal id in %s", tool, raw)
	}
	return core.ProposalID(id)
}

func (e *p4) changeSet() api.ChangeSetDTO {
	e.t.Helper()
	var cs api.ChangeSetDTO
	e.wait("the change set", 30*time.Second, func() (bool, string) {
		code := getJSON(e.t, e.base+"/api/tasks/"+string(e.task)+"/changeset", &cs)
		return code == 200, fmt.Sprint(code)
	})
	return cs
}

func (e *p4) proposal(id core.ProposalID) api.ProposalDTO {
	e.t.Helper()
	var p api.ProposalDTO
	e.must(http.MethodGet, "/api/proposals/"+string(id), nil, &p)
	return p
}

// confirmAll confirms every pending member that a run covers, with the hashes
// the user saw.
func (e *p4) confirmAll(cs api.ChangeSetDTO, ack []string) (int, []byte) {
	e.t.Helper()
	seen := map[string]string{}
	for _, m := range cs.Members {
		switch m.Kind {
		case core.ProposalMergeLocal, core.ProposalRemoveWorktree:
			continue
		}
		if m.Status == core.ProposalPending {
			seen[string(m.ProposalID)] = m.Hash
		}
	}
	return e.do(http.MethodPost, "/api/changesets/"+string(cs.ID)+"/confirm", api.ConfirmRunRequest{Scope: api.ScopeDTO{Kind: "all"}, Seen: seen, Ack: ack}, nil)
}

func (e *p4) waitRunDone(what string) api.ChangeSetDTO {
	e.t.Helper()
	var cs api.ChangeSetDTO
	e.wait(what, 240*time.Second, func() (bool, string) {
		cs = e.changeSet()
		return cs.Run.State == api.RunStateCompleted || cs.Run.State == api.RunStateHalted, fmt.Sprintf("state=%s run=%+v confirming=%+v", cs.State, cs.Run, cs.Confirming)
	})
	if cs.Run.State != api.RunStateCompleted {
		e.t.Fatalf("%s: the run halted: %+v (members %+v)", what, cs.Run, cs.Members)
	}
	return cs
}

func idxOf(order []core.RepoID, r core.RepoID) int {
	return slices.Index(order, r)
}

// shipStep: commits, then linked merge requests, as one change set.
func (e *p4) shipStep(order []string) {
	t := e.t
	t.Helper()
	core1, co, sf := e.repos["shop-core"], e.repos["shop-checkout"], e.repos["shop-storefront"]
	files := map[string]string{"shop-core": p4MoneyPath, "shop-checkout": p4CheckoutPath, "shop-storefront": p4NotesPath}
	subject := func(name string) string { return "feat(" + name + "): currency on the money path" }
	mainHead := map[string]string{}
	for _, name := range order {
		mainHead[name] = e.git(e.repos[name].dir, "rev-parse", "HEAD")
	}

	// --- commits: proposals are created by the agent (MCP); nothing moves ---
	commits := map[string]core.ProposalID{}
	for _, name := range []string{"shop-storefront", "shop-checkout", "shop-core"} { // not the merge order
		commits[name] = e.propose("propose_commit", map[string]any{
			"repo": name, "paths": []string{files[name]}, "message": subject(name), "summary": "Commit the currency change",
		})
	}
	for _, name := range order {
		if e.git(e.repos[name].wtDir, "log", "-1", "--format=%s") == subject(name) {
			t.Fatalf("%s: the commit exists before any confirmation", name)
		}
	}
	cs := e.changeSet()
	t.Logf("change set %s: order=%v suggested=%v cycle=%v warnings=%+v", cs.ID, cs.Order, cs.SuggestedOrder, cs.Cycle, cs.Warnings)
	if len(cs.Members) != 3 {
		t.Fatalf("members = %+v", cs.Members)
	}
	if i, j := idxOf(cs.SuggestedOrder, core1.id), idxOf(cs.SuggestedOrder, co.id); i < 0 || j < 0 || i > j {
		t.Errorf("suggested order %v: shop-core must come before shop-checkout (Maven dependency)", cs.SuggestedOrder)
	}
	e.must(http.MethodPut, "/api/changesets/"+string(cs.ID)+"/order", api.SetOrderRequest{Order: cs.SuggestedOrder}, nil)
	cs = e.changeSet()
	if code, body := e.confirmAll(cs, nil); code >= 300 {
		t.Fatalf("confirm commits = %d %s", code, body)
	}
	cs = e.waitRunDone("the commit run")
	for _, name := range order {
		r := e.repos[name]
		if p := e.proposal(commits[name]); p.Status != core.ProposalExecuted {
			t.Errorf("%s commit = %+v", name, p)
		}
		if got := e.git(r.wtDir, "log", "-1", "--format=%s"); got != subject(name) {
			t.Errorf("%s worktree commit subject = %q", name, got)
		}
		if got := e.git(r.dir, "rev-parse", "HEAD"); got != mainHead[name] {
			t.Errorf("%s: the main checkout moved on commit", name)
		}
	}
	// The CRLF file stayed CRLF through the commit.
	if b := e.git(sf.wtDir, "show", "HEAD:"+p4NotesPath); !strings.Contains(b, "\r") {
		t.Errorf("the committed notes lost their CRLF endings: %q", b)
	}

	// --- merge requests, created in a different order than they are shipped ---
	mrs := map[string]core.ProposalID{}
	for _, name := range []string{"shop-checkout", "shop-storefront", "shop-core"} {
		r := e.repos[name]
		mrs[name] = e.propose("propose_merge_request", map[string]any{
			"repo": name, "branch": r.branch, "remote": "origin", "target": "main",
			"title":       "Currency on the money path (" + name + ")",
			"description": "Part of the currency change. Related merge requests: " + hoststest.LinkMarker,
			"summary":     "Open a merge request for " + name,
		})
	}
	basePushes := map[string]int{}
	for _, name := range order {
		basePushes[name] = len(e.repos[name].host.Pushes())
	}
	cs = e.changeSet()
	rev := []core.RepoID{co.id, core1.id, sf.id}
	e.must(http.MethodPut, "/api/changesets/"+string(cs.ID)+"/order", api.SetOrderRequest{Order: rev}, nil)
	cs = e.changeSet()
	var pf api.PreflightResponse
	e.must(http.MethodPost, "/api/changesets/"+string(cs.ID)+"/preflight", api.PreflightRequest{Scope: api.ScopeDTO{Kind: "all"}}, &pf)
	if len(pf.Warnings) != 1 || pf.Warnings[0].Code != api.WarningDependentBeforeDependency ||
		pf.Warnings[0].Dependent != co.id || pf.Warnings[0].Dependency != core1.id {
		t.Fatalf("preflight of the reversed order = %+v, want one dependent_before_dependency (shop-checkout before shop-core)", pf)
	}
	// Without acknowledging the warning the run is refused and nothing is pushed.
	if code, body := e.confirmAll(cs, nil); code < 400 {
		t.Errorf("a run with an unacknowledged dependency warning was accepted: %d %s", code, body)
	}
	for _, name := range order {
		if n := len(e.repos[name].host.Pushes()); n != basePushes[name] {
			t.Fatalf("%s: %d pushes after a refused run, want %d", name, n, basePushes[name])
		}
	}

	// The right order: dependency first.
	e.must(http.MethodPut, "/api/changesets/"+string(cs.ID)+"/order", api.SetOrderRequest{Order: cs.SuggestedOrder}, nil)
	cs = e.changeSet()
	e.must(http.MethodPost, "/api/changesets/"+string(cs.ID)+"/preflight", api.PreflightRequest{Scope: api.ScopeDTO{Kind: "all"}}, &pf)
	if len(pf.Warnings) != 0 {
		t.Errorf("preflight of the suggested order = %+v, want none", pf)
	}
	if code, body := e.confirmAll(cs, nil); code >= 300 {
		t.Fatalf("confirm merge requests = %d %s", code, body)
	}
	cs = e.waitRunDone("the merge request run")
	if cs.State != api.ChangeSetStateShipped {
		t.Errorf("change set state = %s, want shipped", cs.State)
	}

	// --- what reached the hosts ---
	shipOrder := cs.Order
	nameOf := map[core.RepoID]string{}
	for _, name := range order {
		nameOf[e.repos[name].id] = name
	}
	urls := map[string]string{}
	var everything strings.Builder
	type executed struct {
		name string
		at   time.Time
	}
	var execs []executed
	for _, name := range order {
		p := e.proposal(mrs[name])
		b, _ := json.Marshal(p)
		everything.Write(b)
		if p.Status != core.ProposalExecuted || p.Result == nil || p.Result.MRURL == "" {
			t.Fatalf("%s merge request proposal = %+v", name, p)
		}
		urls[name] = p.Result.MRURL
		if want := e.stub.Server.URL + "/acme/" + name + "/-/merge_requests/1"; p.Result.MRURL != want {
			t.Errorf("%s MR URL = %q, want %q", name, p.Result.MRURL, want)
		}
		entries, err := e.a.store.ListAudit(context.Background(), "proposal:"+string(mrs[name]), 100)
		if err != nil {
			t.Fatal(err)
		}
		var actions []string
		for _, en := range entries {
			actions = append(actions, en.Action)
			everything.WriteString(en.Actor + en.Action + string(en.Detail))
			if en.Action == "proposal.execute" {
				execs = append(execs, executed{name, en.At})
			}
		}
		for _, want := range []string{"proposal.create", "proposal.confirm", "proposal.execute"} {
			if !strings.Contains(strings.Join(actions, " "), want) {
				t.Errorf("%s MR audit %v lacks %s", name, actions, want)
			}
		}
	}
	// Created in merge order.
	slices.SortStableFunc(execs, func(a, b executed) int { return a.at.Compare(b.at) })
	var gotOrder, wantOrder []string
	for _, x := range execs {
		gotOrder = append(gotOrder, x.name)
	}
	for _, id := range shipOrder {
		wantOrder = append(wantOrder, nameOf[id])
	}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("merge requests executed in order %v, want %v", gotOrder, wantOrder)
	}

	for i, name := range wantOrder {
		r := e.repos[name]
		pushes := r.host.Pushes()
		if len(pushes) != basePushes[name]+1 {
			t.Fatalf("%s: %d pushes, want %d", name, len(pushes), basePushes[name]+1)
		}
		last := pushes[len(pushes)-1]
		for _, o := range last.Options {
			everything.WriteString(o + "\n")
		}
		if _, ok := last.Option("merge_request.create"); !ok {
			t.Errorf("%s push options %v lack merge_request.create", name, last.Options)
		}
		if v, _ := last.Option("merge_request.target"); v != "main" {
			t.Errorf("%s target = %q", name, v)
		}
		if !slices.Contains(last.Refs, "refs/heads/"+r.branch) {
			t.Errorf("%s pushed refs %v, want %s", name, last.Refs, r.branch)
		}
		desc, _ := last.Option("merge_request.description")
		if strings.Contains(desc, hoststest.LinkMarker) || strings.Contains(desc, "ygg:links") {
			t.Errorf("%s description still has the link marker: %q", name, desc)
		}
		for j, other := range wantOrder {
			has := strings.Contains(desc, urls[other])
			switch {
			case j < i && !has:
				t.Errorf("%s (position %d) description %q lacks the earlier MR %s", name, i, desc, urls[other])
			case j >= i && has:
				t.Errorf("%s (position %d) description %q links the later or own MR %s", name, i, desc, urls[other])
			}
		}
		// The pushed branch is the worktree's commit.
		if got, want := strings.TrimSpace(r.host.MustGit(r.host.Dir, "rev-parse", "refs/heads/"+r.branch)), e.git(r.wtDir, "rev-parse", "HEAD"); got != want {
			t.Errorf("%s remote branch = %s, want %s", name, got, want)
		}
	}
	for _, rq := range e.stub.Requests() {
		if rq.Kind == "create_mr" || rq.Kind == "update_mr" {
			t.Errorf("a merge request was created through the API stub (%+v): they go through push options", rq)
		}
	}

	// --- change set and task audit, and no secret anywhere ---
	for _, subj := range []string{"changeset:" + string(cs.ID), "task:" + string(e.task)} {
		entries, err := e.a.store.ListAudit(context.Background(), subj, 200)
		if err != nil {
			t.Fatal(err)
		}
		var actions []string
		for _, en := range entries {
			actions = append(actions, en.Action)
			everything.WriteString(en.Actor + en.Action + string(en.Detail))
		}
		t.Logf("audit %s: %v", subj, actions)
	}
	csJSON, _ := json.Marshal(cs)
	everything.Write(csJSON)
	for _, bad := range []string{p4Token, "glpat-", "ghp_", "://user:"} {
		if strings.Contains(everything.String(), bad) {
			t.Errorf("a secret-looking value %q is in proposals, change set, audit or push options", bad)
		}
	}
}

// landStep: merge_local and remove_worktree are proposals; nothing happens
// until the user confirms (RV-05, RV-06).
func (e *p4) landStep(r *p4Repo) {
	t := e.t
	t.Helper()
	mainHead := e.git(r.dir, "rev-parse", "HEAD")
	pushes := len(r.host.Pushes())

	var merge, remove api.ProposalDTO
	e.must(http.MethodPost, "/api/worktrees/"+string(r.wt)+"/merge-local", api.MergeLocalRequest{}, &merge)
	if merge.Status != core.ProposalPending || merge.Kind != core.ProposalMergeLocal {
		t.Fatalf("merge-local proposal = %+v", merge)
	}
	code, body := e.do(http.MethodPost, "/api/worktrees/"+string(r.wt)+"/remove", nil, &remove)
	removeEarly := code < 300
	if removeEarly && (remove.Status != core.ProposalPending || remove.Kind != core.ProposalRemoveWorktree) {
		t.Fatalf("remove proposal = %+v", remove)
	}
	t.Logf("remove proposal before the merge: %d %s", code, body)
	time.Sleep(500 * time.Millisecond)
	if got := e.git(r.dir, "rev-parse", "HEAD"); got != mainHead {
		t.Fatal("the main checkout moved before the merge was confirmed")
	}
	if _, err := os.Stat(r.wtDir); err != nil {
		t.Fatalf("the worktree folder changed before anything was confirmed: %v", err)
	}

	confirm := func(p api.ProposalDTO) {
		t.Helper()
		e.must(http.MethodPost, "/api/proposals/"+string(p.ID)+"/confirm", api.ConfirmProposalRequest{Hash: p.ActionHash}, nil)
		e.wait("proposal "+string(p.Kind), 180*time.Second, func() (bool, string) {
			p = e.proposal(p.ID)
			if p.Status == core.ProposalFailed {
				t.Fatalf("%s failed: %+v", p.Kind, p)
			}
			return p.Status == core.ProposalExecuted, string(p.Status)
		})
	}
	confirm(merge)
	if got := e.git(r.dir, "rev-parse", "HEAD"); got == mainHead {
		t.Fatal("the confirmed merge did not move the main checkout")
	}
	if parents := strings.Fields(e.git(r.dir, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Errorf("the merge commit has parents %v, want a --no-ff merge commit with two", parents)
	}
	if st := e.git(r.dir, "status", "--porcelain"); st != "" {
		t.Errorf("the main checkout is dirty after the merge: %q", st)
	}
	if n := len(r.host.Pushes()); n != pushes {
		t.Errorf("the local merge pushed (%d pushes, was %d)", n, pushes)
	}
	if _, err := os.Stat(r.wtDir); err != nil {
		t.Errorf("the merge removed the worktree: %v", err)
	}

	if !removeEarly {
		e.must(http.MethodPost, "/api/worktrees/"+string(r.wt)+"/remove", nil, &remove)
	}
	if _, err := os.Stat(r.wtDir); err != nil {
		t.Fatalf("the worktree is gone before the removal was confirmed: %v", err)
	}
	confirm(remove)
	if _, err := os.Stat(r.wtDir); !os.IsNotExist(err) {
		t.Errorf("the worktree folder is still there after the confirmed removal: %v", err)
	}
	if out := e.git(r.dir, "branch", "--list", r.branch); !strings.Contains(out, r.branch) {
		t.Errorf("branch %s was deleted by the removal (%q)", r.branch, out)
	}
	staging := filepath.Join(e.cfg.dataDir, "wt", landing.StagingDir)
	if ents, err := os.ReadDir(staging); err == nil && len(ents) != 0 {
		t.Errorf("%s is not empty after the removal: %v", staging, ents)
	}
	if wl := e.git(r.dir, "worktree", "list", "--porcelain"); strings.Contains(wl, filepath.ToSlash(r.wtDir)) {
		t.Errorf("git still lists the removed worktree:\n%s", wl)
	}
}
