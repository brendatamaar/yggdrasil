package proposals

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

var mrHead = strings.Repeat("a", 40)

const mrMarker = "0123456789abcdef"

// p6 is a world with the three Phase 6 fake ports wired in.
type p6 struct {
	*world
	ign  *proposalstest.IgnoreFake
	host *proposalstest.CommentFake
	pull *proposalstest.PullFake[PullFFResult]
}

func newP6(t *testing.T, secrets ...string) *p6 {
	t.Helper()
	w := newWorld(t, secrets...)
	x := &p6{
		world: w,
		ign:   proposalstest.NewIgnoreFake(),
		host:  proposalstest.NewCommentFake(mrHead, coreprop.CommentBodyHash),
		pull:  &proposalstest.PullFake[PullFFResult]{},
	}
	x.pull.Result = func(c proposalstest.PullCall) PullFFResult {
		return PullFFResult{NewHead: strings.Repeat("b", 40), Note: "Fast-forwarded."}
	}
	w.cfg.Ignore, w.cfg.Comments, w.cfg.Pull = x.ign, x.host, x.pull
	w.svc.Close()
	w.start()
	return x
}

// ignoreAction seeds the main checkout's .gitignore (when present) and returns
// the action that was reviewed against it.
func (x *p6) ignoreAction(content string, present bool, entries ...string) coreprop.IgnoreEntries {
	x.t.Helper()
	x.ign.Seed(x.t, x.mainDir, []byte(content), present)
	if len(entries) == 0 {
		entries = []string{"/dist/", "/node_modules/"}
	}
	a := coreprop.IgnoreEntries{Where: coreprop.MainCheckout{Repo: x.repo.ID}, Entries: entries}
	if present {
		a.ExpectedHash = platform.HashHex([]byte(content))
	} else {
		a.ExpectedAbsent = true
	}
	return a
}

func (x *p6) commentAction(body string) coreprop.ReviewComment {
	return coreprop.ReviewComment{Repo: x.repo.ID, MRNumber: 7, ExpectedHead: mrHead, Body: body, Marker: mrMarker}
}

func (x *p6) pullAction() coreprop.PullFF {
	return coreprop.PullFF{Repo: x.repo.ID, Branch: "main", Upstream: "origin/main", ExpectHead: x.head(x.mainDir), Verify: true}
}

// ports is the number of calls that reached any of the three ports.
func (x *p6) ports() int {
	return x.ign.Calls() + x.host.Calls() + len(x.pull.Calls())
}

func (x *p6) ignoreText() string {
	x.t.Helper()
	b, _ := x.ign.Content(x.t, x.mainDir)
	return string(b)
}

func (x *p6) effects(id core.ProposalID) []string {
	x.t.Helper()
	es, err := x.store.ListEffectsByProposal(x.ctx, id)
	if err != nil {
		x.t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, string(e.State))
	}
	return out
}

func (x *p6) countAudit(id core.ProposalID, action string) int {
	n := 0
	for _, a := range x.auditActions(id) {
		if a == action {
			n++
		}
	}
	return n
}

// restart simulates the process dying and starting again on the same database.
func (x *p6) restart() {
	x.svc.Close()
	x.start()
}

func lockedErr() error { return errors.Join(platform.ErrLocked, errors.New("held")) }

func context_deadline() error { return context.DeadlineExceeded }

// proposalOf loads the stored proposal.
func proposalOf(t *testing.T, w *world, id core.ProposalID) coreprop.Proposal {
	t.Helper()
	rec, err := w.store.GetProposalRecord(w.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Proposal
}

// waitForAttempt waits until the proposal has failed on attempt n.
func (x *p6) waitForAttempt(id core.ProposalID, n int) View {
	x.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		v, err := x.svc.Get(x.ctx, id)
		if err != nil {
			x.t.Fatal(err)
		}
		if v.Status == core.ProposalFailed && v.Attempt == n {
			return v
		}
		if time.Now().After(deadline) {
			x.t.Fatalf("proposal %s is %s attempt %d, want failed on attempt %d", id, v.Status, v.Attempt, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
