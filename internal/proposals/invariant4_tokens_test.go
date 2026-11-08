package proposals

import (
	"fmt"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/changesets"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// everything a viewer or a later reader could see about a proposal: the view
// (message, result, lines), the audit rows of the proposal, and the events.
func (w *world) visibleText(id core.ProposalID) string {
	w.t.Helper()
	v, err := w.svc.Get(w.ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%+v\n", v)
	if v.Result != nil {
		fmt.Fprintf(&b, "%+v\n", *v.Result)
	}
	rows, err := w.store.ListAudit(w.ctx, "", 500)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "%s %s %s %s\n", r.Actor, r.Action, r.Subject, r.Detail)
	}
	w.pub.mu.Lock()
	fmt.Fprintf(&b, "%+v\n", w.pub.events)
	w.pub.mu.Unlock()
	return b.String()
}

// Invariant 4 (docs/AGENTS.md 6): a token never appears in a URL, a log or the
// audit log. Two ways it could get into a proposal: the remote URL carries it
// (the push must be refused and say nothing about it), and a link or branch
// name carries it (the stored result, the note and the audit rows must not).
func TestInvariant_4_tokens_never_reach_stored_proposal_data(t *testing.T) {
	t.Run("a remote URL with a token is refused and not echoed", func(t *testing.T) {
		w := newWorld(t, testToken)
		w.git.SetRemoteURL(w.wtDir, "origin", "https://oauth2:"+testToken+"@gitlab.example.test/acme/shop-api.git")
		a := coreprop.MergeRequest{Where: coreprop.InWorktree{ID: wtID}, Branch: branch, Remote: "origin", TargetBranch: "main",
			Title: "t", Description: "d", ExpectHead: w.head(w.wtDir)}
		v, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: a, Summary: "because", Source: coreprop.AgentSource{Task: w.task.ID}})
		if err != nil {
			// Refused at Create: the error text must not carry it either.
			if strings.Contains(err.Error(), testToken) {
				t.Fatalf("refusal echoes the token: %v", err)
			}
			return
		}
		w.confirm(v)
		got := w.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailUnsafeRemote) && !strings.Contains(strings.ToLower(got.Message), "remote") {
			t.Errorf("the push was not refused for its remote: %+v", got)
		}
		if text := w.visibleText(v.ID); strings.Contains(text, testToken) {
			t.Errorf("the token is visible after a refused push:\n%s", text)
		}
		if n := len(w.git.pushes); n != 0 {
			t.Errorf("%d push(es) ran against a remote with a token in it", n)
		}
	})

	t.Run("links and branches that carry a token are redacted in the stored data", func(t *testing.T) {
		w := newWorld(t, testToken)
		tokenURL := "https://oauth2:" + testToken + "@gitlab.example.test/acme/shop-web/-/merge_requests/7?private_token=" + testToken
		w.withLinks(fakeLinks{sl: SetLinks{
			Order:    []core.RepoID{"shop-web", w.repo.ID},
			Branches: map[core.RepoID]string{"shop-web": "ygg/" + testToken, w.repo.ID: branch},
			URLs:     map[core.RepoID]string{"shop-web": tokenURL},
		}})
		got, _ := w.runMR(changesets.LinkMarker)
		text := w.visibleText(got.ID)
		if strings.Contains(text, testToken) {
			t.Errorf("the token is visible in the proposal's stored data:\n%s", text)
		}
		if got.Result == nil || strings.Contains(got.Result.MRURL, "@") {
			t.Errorf("stored MR URL = %+v", got.Result)
		}
	})
}

// Regression (found by test-engineer T2, fixed): fillLinks once redacted the
// note it records but not the description it pushed to the host, so a secret
// that is part of a branch name in the change set is sent as MR text. Branch
// names come from the host or the agent, so a token there is unlikely, but the
// description was the one outgoing text that skipped s.redact.
func TestMRDescriptionIsRedactedBeforeItIsPushed(t *testing.T) {
	w := newWorld(t, testToken)
	w.withLinks(fakeLinks{sl: SetLinks{
		Order:    []core.RepoID{"shop-web", w.repo.ID},
		Branches: map[core.RepoID]string{"shop-web": "ygg/" + testToken, w.repo.ID: branch},
		URLs:     map[core.RepoID]string{},
	}})
	w.runMR(changesets.LinkMarker)
	if desc := w.pushedDescription(); strings.Contains(desc, testToken) {
		t.Errorf("the pushed MR description carries the token: %q", desc)
	}
}
