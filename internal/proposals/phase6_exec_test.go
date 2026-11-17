package proposals

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

// ---- creation ----

func TestPhase6CreateRefusesWithoutThePortAndForDisabledKinds(t *testing.T) {
	w := newWorld(t) // no Phase 6 ports
	for _, a := range []coreprop.Action{
		coreprop.IgnoreEntries{Where: coreprop.MainCheckout{Repo: w.repo.ID}, Entries: []string{"/dist/"}, ExpectedAbsent: true},
		coreprop.ReviewComment{Repo: w.repo.ID, MRNumber: 1, ExpectedHead: mrHead, Body: "x", Marker: mrMarker},
		coreprop.PullFF{Repo: w.repo.ID, Branch: "main", Upstream: "origin/main", ExpectHead: mrHead},
		coreprop.PublishRepo{Operation: "op_1", Repo: w.repo.ID, HostInstance: "github.com", Owner: "acme", OwnerKind: "org", Name: "x", Visibility: "private", Branch: "main", ExpectedHead: mrHead, Marker: mrMarker},
	} {
		_, err := w.svc.Create(w.ctx, CreateRequest{Task: w.task.ID, Action: a, Source: coreprop.AgentSource{Task: w.task.ID}})
		if !errors.Is(err, ErrRefused) {
			t.Errorf("Create(%s) = %v, want a refusal", a.Kind(), err)
		}
	}
	if n, _ := w.svc.PendingCount(w.ctx, w.task.ID); n != 0 {
		t.Errorf("%d proposals were stored for kinds that cannot run", n)
	}
}

func TestPhase6CreateStoresPendingProposalsAndTouchesNoPort(t *testing.T) {
	x := newP6(t)
	for _, a := range []coreprop.Action{x.ignoreAction("*.log\n", true), x.commentAction("Looks fine."), x.pullAction()} {
		v := x.create(a)
		if v.Status != core.ProposalPending || v.ActionHash == "" || len(v.Lines) == 0 {
			t.Errorf("%s: view = %+v", a.Kind(), v)
		}
	}
	if n := x.ports(); n != 0 {
		t.Fatalf("%d port calls while only creating proposals", n)
	}
}

func TestPhase6CreateRefusesAPullOfAnotherBranch(t *testing.T) {
	x := newP6(t)
	a := x.pullAction()
	a.Branch = "feature"
	a.Upstream = "origin/feature"
	if _, err := x.svc.Create(x.ctx, CreateRequest{Task: x.task.ID, Action: a}); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

// ---- nothing before a matching confirm (invariant 2) ----

// TestPropNoWriteBeforeConfirm: whatever happens to a proposal before a
// matching confirmation (nothing, a wrong hash, a rejection, a duplicate
// reject), no executor reaches its port.
func TestPropNoWriteBeforeConfirm(t *testing.T) {
	for _, kind := range []string{"ignore", "comment", "pull"} {
		for seed := int64(1); seed <= 6; seed++ {
			t.Run(fmt.Sprintf("%s/%d", kind, seed), func(t *testing.T) {
				x := newP6(t)
				r := rand.New(rand.NewSource(seed))
				var a coreprop.Action
				seeded := x.ignoreAction("*.log\n", true)
				switch kind {
				case "ignore":
					a = seeded
				case "comment":
					a = x.commentAction("Looks fine.")
				default:
					a = x.pullAction()
				}
				v := x.create(a)
				for i := 0; i < 1+r.Intn(4); i++ {
					bad := strings.Repeat("0", 64)
					if r.Intn(2) == 0 {
						bad = v.ActionHash[:63] + "x"
					}
					if _, err := x.svc.Confirm(x.ctx, v.ID, bad, "user"); !errors.Is(err, ErrRefused) {
						t.Fatalf("a wrong hash was accepted: %v", err)
					}
				}
				if r.Intn(2) == 0 {
					if _, err := x.svc.Reject(x.ctx, v.ID, "user"); err != nil {
						t.Fatal(err)
					}
					if _, err := x.svc.Confirm(x.ctx, v.ID, v.ActionHash, "user"); err == nil {
						t.Fatal("a rejected proposal was confirmed")
					}
				}
				x.restart() // a restart executes nothing either
				if n := x.ports(); n != 0 {
					t.Fatalf("%d port calls before a matching confirm", n)
				}
				if x.ignoreText() != "*.log\n" {
					t.Fatalf(".gitignore = %q", x.ignoreText())
				}
			})
		}
	}
}

// Invariant 2: a pending proposal never changes anything on its own, however
// long the service runs.
func TestInvariant_2_Phase6NothingMutatesWithoutConfirmation(t *testing.T) {
	x := newP6(t)
	x.create(x.ignoreAction("*.log\n", true))
	x.create(x.commentAction("Looks fine."))
	x.create(x.pullAction())
	x.restart()
	if x.ports() != 0 || len(x.host.Posted()) != 0 || x.ignoreText() != "*.log\n" {
		t.Fatal("something ran without a confirmation")
	}
}

// ---- IgnoreEntries ----

func TestIgnoreAppendsAfterConfirmAndAuditsEveryStage(t *testing.T) {
	x := newP6(t)
	v := x.create(x.ignoreAction("*.log\r\n", true))
	x.confirm(v)
	got := x.waitFor(v.ID, core.ProposalExecuted)
	if want := "*.log\r\n/dist/\r\n/node_modules/\r\n"; x.ignoreText() != want {
		t.Fatalf(".gitignore = %q, want %q", x.ignoreText(), want)
	}
	if got.Result == nil || !strings.Contains(got.Result.Note, "2 entries") {
		t.Fatalf("result = %+v", got.Result)
	}
	acts := x.auditActions(v.ID)
	want := []string{"proposal.create", "proposal.confirm", "proposal.effect", "proposal.effect", "proposal.execute"}
	if strings.Join(acts, ",") != strings.Join(want, ",") {
		t.Fatalf("audit = %v, want %v", acts, want)
	}
	if x.mainLockHeld() {
		t.Fatal("the main lock was not released")
	}
}

func (x *p6) mainLockHeld() bool {
	_, holder, ok := x.lock.TryAcquire([]core.RepoID{x.repo.ID}, mainlockBuild("probe"))
	_ = holder
	return !ok
}

func TestIgnoreCreatesAMissingFile(t *testing.T) {
	x := newP6(t)
	v := x.create(x.ignoreAction("", false, "/dist/"))
	x.confirm(v)
	x.waitFor(v.ID, core.ProposalExecuted)
	if x.ignoreText() != "/dist/\n" {
		t.Fatalf(".gitignore = %q", x.ignoreText())
	}
}

func TestIgnoreInUseByAnotherJobIsRetryableAndWritesNothing(t *testing.T) {
	x := newP6(t)
	v := x.create(x.ignoreAction("*.log\n", true))
	release, _, ok := x.lock.TryAcquire([]core.RepoID{x.repo.ID}, mainlockBuild("b1"))
	if !ok {
		t.Fatal("could not hold the lock")
	}
	x.confirm(v)
	got := x.waitFor(v.ID, core.ProposalFailed)
	release()
	if !got.Retryable || x.ign.Calls() != 0 {
		t.Fatalf("failure = %+v, port calls %d", got, x.ign.Calls())
	}
	if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	x.waitFor(v.ID, core.ProposalExecuted)
}

// TestPropStaleHashRefused: whatever the file became after the review, nothing
// is written and the failure is Stale and not retryable.
func TestPropStaleHashRefused(t *testing.T) {
	for seed := int64(1); seed <= 10; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			x := newP6(t)
			r := rand.New(rand.NewSource(seed))
			present := r.Intn(2) == 0
			a := x.ignoreAction("*.log\n", present)
			v := x.create(a)
			var changed string
			switch {
			case present && r.Intn(2) == 0:
				changed = "*.log\n*.tmp\n" // edited
			case present:
				changed = "" // emptied: an empty file is not an absent one
			default:
				changed = "created meanwhile\n"
			}
			x.ign.Seed(t, x.mainDir, []byte(changed), true)
			x.confirm(v)
			got := x.waitFor(v.ID, core.ProposalFailed)
			if got.FailKind != string(coreprop.FailStale) || got.Retryable {
				t.Fatalf("failure = %+v", got)
			}
			if x.ignoreText() != changed {
				t.Fatalf(".gitignore = %q, want it untouched %q", x.ignoreText(), changed)
			}
			if x.ign.Appends() != 0 {
				t.Fatal("an append was attempted on a changed file")
			}
		})
	}
}

func TestIgnoreWriterRefusalsAreMappedAndNothingIsRepaired(t *testing.T) {
	cases := map[string]struct {
		err       error
		kind      coreprop.FailKind
		retryable bool
	}{
		"locked":      {lockedErr(), coreprop.FailExec, true},
		"link":        {platform.ErrUnsafePath, coreprop.FailPrecondition, false},
		"too large":   {platform.ErrTooLarge, coreprop.FailPrecondition, false},
		"unsupported": {platform.ErrUnsupportedPlatform, coreprop.FailPrecondition, false},
		"changed":     {platform.ErrChanged, coreprop.FailStale, false},
		"other":       {errors.New("disk exploded at /home/someone/secret"), coreprop.FailInterrupted, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			x := newP6(t)
			v := x.create(x.ignoreAction("*.log\n", true))
			x.ign.Script(proposalstest.FailBefore, c.err)
			x.confirm(v)
			got := x.waitFor(v.ID, core.ProposalFailed)
			if got.FailKind != string(c.kind) || got.Retryable != c.retryable {
				t.Fatalf("failure = %+v", got)
			}
			if x.ignoreText() != "*.log\n" {
				t.Fatalf(".gitignore = %q", x.ignoreText())
			}
			if strings.Contains(got.Message, "/home/") {
				t.Fatalf("message shows a machine path: %q", got.Message)
			}
		})
	}
}

func TestIgnoreLostReplyThenRetryNeverAppendsTwice(t *testing.T) {
	x := newP6(t)
	v := x.create(x.ignoreAction("*.log\n", true))
	x.ign.Script(proposalstest.FailAfter, context_deadline())
	x.confirm(v)
	got := x.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable {
		t.Fatalf("failure = %+v", got)
	}
	landed := x.ignoreText()
	if strings.Count(landed, "/dist/") != 1 {
		t.Fatalf("the write did not land once: %q", landed)
	}
	if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	again := x.waitForAttempt(v.ID, 2)
	if again.FailKind != string(coreprop.FailStale) || x.ignoreText() != landed {
		t.Fatalf("retry = %+v, file %q", again, x.ignoreText())
	}
}

// ---- duplicate confirm, reject, stale ----

func TestPropDuplicateConfirmNoSecondEffect(t *testing.T) {
	x := newP6(t)
	cv := x.create(x.commentAction("Looks fine."))
	iv := x.create(x.ignoreAction("*.log\n", true))
	pv := x.create(x.pullAction())
	for _, v := range []View{cv, iv, pv} {
		x.confirm(v)
		x.waitFor(v.ID, core.ProposalExecuted)
		for i := 0; i < 3; i++ {
			if _, err := x.svc.Confirm(x.ctx, v.ID, v.ActionHash, "user"); !errors.Is(err, ErrAlreadyDecided) {
				t.Fatalf("duplicate confirm of %s = %v", v.Kind, err)
			}
			if _, err := x.svc.Reject(x.ctx, v.ID, "user"); !errors.Is(err, ErrAlreadyDecided) {
				t.Fatalf("reject of an executed %s = %v", v.Kind, err)
			}
			if _, err := x.svc.Retry(x.ctx, v.ID, "user"); !errors.Is(err, ErrAlreadyDecided) {
				t.Fatalf("retry of an executed %s = %v", v.Kind, err)
			}
		}
	}
	x.restart()
	if x.host.Posts() != 1 || x.ign.Appends() != 1 || len(x.pull.Calls()) != 1 {
		t.Fatalf("posts %d, appends %d, pulls %d; want one each", x.host.Posts(), x.ign.Appends(), len(x.pull.Calls()))
	}
	if strings.Count(x.ignoreText(), "/dist/") != 1 {
		t.Fatalf(".gitignore = %q", x.ignoreText())
	}
}

func TestRejectedProposalsNeverReachAPort(t *testing.T) {
	x := newP6(t)
	v := x.create(x.commentAction("Looks fine."))
	if _, err := x.svc.Reject(x.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.Confirm(x.ctx, v.ID, v.ActionHash, "user"); err == nil {
		t.Fatal("confirmed after a rejection")
	}
	if x.ports() != 0 {
		t.Fatalf("%d port calls", x.ports())
	}
}

func TestUntrackedRepoStopsAllThreeBeforeAnyPort(t *testing.T) {
	x := newP6(t)
	vs := []View{x.create(x.ignoreAction("*.log\n", true)), x.create(x.commentAction("Looks fine.")), x.create(x.pullAction())}
	x.res.setUntracked(true)
	for _, v := range vs {
		if _, err := x.svc.Confirm(x.ctx, v.ID, v.ActionHash, "user"); !errors.Is(err, ErrRefused) {
			t.Fatalf("confirm of %s on an untracked repo = %v", v.Kind, err)
		}
	}
	// Untracked between confirm and execution: the executor's own check.
	x.res.setUntracked(false)
	x2 := vs[1]
	x.confirmWithoutRunning(x2)
	x.res.setUntracked(true)
	x.restart()
	x.waitFor(x2.ID, core.ProposalFailed)
	if x.ports() != 0 {
		t.Fatalf("%d port calls for an untracked repo", x.ports())
	}
}

// ---- ReviewComment ----

func TestCommentPostsOnceAfterConfirmWithEffectRecordedFirst(t *testing.T) {
	x := newP6(t)
	v := x.create(x.commentAction("Looks fine.\nOne nit."))
	x.confirm(v)
	got := x.waitFor(v.ID, core.ProposalExecuted)
	posted := x.host.Posted()
	if len(posted) != 1 || posted[0].Marker != mrMarker || posted[0].Body != "Looks fine.\nOne nit." || posted[0].Head != mrHead {
		t.Fatalf("posted = %+v", posted)
	}
	if es := x.effects(v.ID); len(es) != 1 || es[0] != "succeeded" {
		t.Fatalf("effects = %v", es)
	}
	if got.Result == nil || !strings.Contains(got.Result.Note, posted[0].ID) {
		t.Fatalf("result = %+v", got.Result)
	}
	if x.countAudit(v.ID, "proposal.effect") != 2 {
		t.Fatalf("audit = %v", x.auditActions(v.ID))
	}
}

// TestPropStaleHeadRefused: a head that moved since the review means nothing is
// posted and the failure is Stale and final.
func TestPropStaleHeadRefused(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			x := newP6(t)
			r := rand.New(rand.NewSource(seed))
			v := x.create(x.commentAction("Looks fine."))
			moved := strings.Repeat(string("bcdef0"[r.Intn(6)]), 40)
			x.host.SetHead(moved)
			x.confirm(v)
			got := x.waitFor(v.ID, core.ProposalFailed)
			if got.FailKind != string(coreprop.FailStale) || got.Retryable {
				t.Fatalf("failure = %+v", got)
			}
			if x.host.Posts() != 0 || len(x.effects(v.ID)) != 0 {
				t.Fatalf("posts %d, effects %v", x.host.Posts(), x.effects(v.ID))
			}
		})
	}
}

func TestCommentHeadReadFailureIsRetryableAndPostsNothing(t *testing.T) {
	x := newP6(t)
	v := x.create(x.commentAction("Looks fine."))
	x.host.ScriptHead(errors.New("host down"))
	x.confirm(v)
	got := x.waitFor(v.ID, core.ProposalFailed)
	if !got.Retryable || x.host.Posts() != 0 {
		t.Fatalf("failure = %+v, posts %d", got, x.host.Posts())
	}
	x.host.ScriptHead(nil)
	if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
		t.Fatal(err)
	}
	x.waitFor(v.ID, core.ProposalExecuted)
	if x.host.Posts() != 1 {
		t.Fatalf("posts = %d", x.host.Posts())
	}
}

func TestCommentDefiniteRejectionIsADefiniteFailure(t *testing.T) {
	x := newP6(t, testToken)
	v := x.create(x.commentAction("Looks fine."))
	x.host.ScriptPost(proposalstest.FailBefore, proposalstest.RefusedError{Msg: "The host refused " + testToken})
	x.confirm(v)
	got := x.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailRemote) || len(x.host.Posted()) != 0 || x.host.Finds() != 0 {
		t.Fatalf("failure = %+v, posted %d, finds %d", got, len(x.host.Posted()), x.host.Finds())
	}
	if es := x.effects(v.ID); len(es) != 1 || es[0] != "failed" {
		t.Fatalf("effects = %v", es)
	}
	if strings.Contains(x.visibleText(v.ID), testToken) {
		t.Fatal("a token is visible")
	}
}

// TestPropUnknownNeverBlindRetry: after an unknown outcome the executor reads
// before it ever sends again; a comment found is done, an absent one needs a
// human retry that posts once, and a conflict or an unreadable list blocks any
// POST.
func TestPropUnknownNeverBlindRetry(t *testing.T) {
	unknownErr := fmt.Errorf("lost: %w", ErrOutcomeUnknown)
	t.Run("lost reply, comment is there: found, one comment, no repost", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.commentAction("Looks fine."))
		x.host.ScriptPost(proposalstest.FailAfter, unknownErr)
		x.confirm(v)
		got := x.waitFor(v.ID, core.ProposalExecuted)
		if x.host.Posts() != 1 || len(x.host.Posted()) != 1 {
			t.Fatalf("posts %d, comments %d", x.host.Posts(), len(x.host.Posted()))
		}
		if es := x.effects(v.ID); len(es) != 1 || es[0] != "succeeded" {
			t.Fatalf("effects = %v", es)
		}
		if !strings.Contains(got.Result.Note, x.host.Posted()[0].ID) {
			t.Fatalf("note = %q", got.Result.Note)
		}
	})
	t.Run("request never arrived: absent, human retry posts exactly once", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.commentAction("Looks fine."))
		x.host.ScriptPost(proposalstest.FailBefore, unknownErr)
		x.confirm(v)
		got := x.waitFor(v.ID, core.ProposalFailed)
		if !got.Retryable || x.host.Posts() != 1 || len(x.host.Posted()) != 0 {
			t.Fatalf("failure = %+v, posts %d", got, x.host.Posts())
		}
		findsBefore := x.host.Finds()
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitFor(v.ID, core.ProposalExecuted)
		if x.host.Finds() <= findsBefore {
			t.Fatal("the retry posted without reading the host first")
		}
		if x.host.Posts() != 2 || len(x.host.Posted()) != 1 {
			t.Fatalf("posts %d, comments %d", x.host.Posts(), len(x.host.Posted()))
		}
	})
	for name, answer := range map[string]coreprop.CommentLookup{
		"conflict":   {Outcome: coreprop.CommentConflict},
		"unreadable": {Outcome: coreprop.CommentUnknown},
	} {
		t.Run("a "+name+" lookup blocks every resend", func(t *testing.T) {
			x := newP6(t)
			v := x.create(x.commentAction("Looks fine."))
			x.host.ScriptPost(proposalstest.FailBefore, unknownErr)
			x.host.ScriptFind(answer, nil)
			x.confirm(v)
			got := x.waitFor(v.ID, core.ProposalFailed)
			if !got.Retryable || x.host.Posts() != 1 {
				t.Fatalf("failure = %+v, posts %d", got, x.host.Posts())
			}
			// The retry's own lookup is blocked too.
			x.host.ScriptFind(answer, nil)
			if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
				t.Fatal(err)
			}
			x.waitForAttempt(v.ID, 2)
			if x.host.Posts() != 1 {
				t.Fatalf("a blocked lookup was followed by a POST (%d)", x.host.Posts())
			}
		})
	}
	t.Run("a lookup error blocks the resend", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.commentAction("Looks fine."))
		x.host.ScriptPost(proposalstest.FailBefore, unknownErr)
		x.host.ScriptFind(coreprop.CommentLookup{}, errors.New("host down"))
		x.confirm(v)
		got := x.waitFor(v.ID, core.ProposalFailed)
		if !got.Retryable || x.host.Posts() != 1 {
			t.Fatalf("failure = %+v, posts %d", got, x.host.Posts())
		}
	})
	t.Run("an error nobody classified is treated as unknown, not as a refusal", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.commentAction("Looks fine."))
		x.host.ScriptPost(proposalstest.FailAfter, errors.New("EOF"))
		x.confirm(v)
		x.waitFor(v.ID, core.ProposalExecuted)
		if x.host.Posts() != 1 || len(x.host.Posted()) != 1 {
			t.Fatalf("posts %d, comments %d", x.host.Posts(), len(x.host.Posted()))
		}
	})
}

// ---- PullFF ----

func TestPullAfterConfirmCallsTheRunnerWithTheActionAndRecordsTheBuild(t *testing.T) {
	x := newP6(t)
	a := x.pullAction()
	x.pull.Result = func(proposalstest.PullCall) PullFFResult {
		return PullFFResult{NewHead: strings.Repeat("c", 40), Note: "Fast-forwarded 3 commits.", BuildID: "bld_one"}
	}
	v := x.create(a)
	x.confirm(v)
	got := x.waitFor(v.ID, core.ProposalExecuted)
	calls := x.pull.Calls()
	if len(calls) != 1 || calls[0] != (proposalstest.PullCall{Repo: a.Repo, Branch: "main", Upstream: "origin/main", ExpectHead: a.ExpectHead, Verify: true}) {
		t.Fatalf("calls = %+v", calls)
	}
	if got.Result.BuildID != "bld_one" || got.Result.CommitID != strings.Repeat("c", 40) || !strings.Contains(got.Result.Note, "3 commits") {
		t.Fatalf("result = %+v", got.Result)
	}
	if x.countAudit(v.ID, "proposal.effect") != 2 {
		t.Fatalf("audit = %v", x.auditActions(v.ID))
	}
}

func TestPullRefusesWhenTheTipOrBranchChangedAndNeverCallsTheRunner(t *testing.T) {
	x := newP6(t)
	v := x.create(x.pullAction())
	x.git.Write(x.mainDir, "z.txt", "z\n")
	_ = x.git.Fake.Add(x.ctx, x.mainDir, []string{"z.txt"})
	if _, err := x.git.Fake.Commit(x.ctx, x.mainDir, []string{"z.txt"}, "local", ""); err != nil {
		t.Fatal(err)
	}
	x.confirm(v)
	got := x.waitFor(v.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailStale) || got.Retryable || len(x.pull.Calls()) != 0 {
		t.Fatalf("failure = %+v, calls %d", got, len(x.pull.Calls()))
	}

	v2 := x.create(x.pullAction())
	x.git.setCurrentBranch(x.mainDir, "other")
	x.confirm(v2)
	got = x.waitFor(v2.ID, core.ProposalFailed)
	if got.FailKind != string(coreprop.FailPrecondition) || len(x.pull.Calls()) != 0 {
		t.Fatalf("failure = %+v", got)
	}
}

type userErr struct{ msg string }

func (e userErr) Error() string       { return "internal " + e.msg }
func (e userErr) UserMessage() string { return e.msg }

func TestPullRunnerRefusalsAreMappedAndFallBackToNothing(t *testing.T) {
	cases := map[string]struct {
		err       error
		kind      coreprop.FailKind
		retryable bool
	}{
		"head changed": {fmt.Errorf("x: %w", ErrPullHeadChanged), coreprop.FailStale, false},
		"busy":         {ErrPullBusy, coreprop.FailPrecondition, true},
		"diverged":     {userErr{"The branch has diverged."}, coreprop.FailPrecondition, true},
		"unclassified": {errors.New("weird"), coreprop.FailExec, true},
		"timeout":      {context_deadline(), coreprop.FailInterrupted, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			x := newP6(t)
			v := x.create(x.pullAction())
			x.pull.Script(proposalstest.FailBefore, c.err)
			x.confirm(v)
			got := x.waitFor(v.ID, core.ProposalFailed)
			if got.FailKind != string(c.kind) || got.Retryable != c.retryable {
				t.Fatalf("failure = %+v", got)
			}
			if strings.Contains(got.Message, "internal") || strings.Contains(got.Message, "weird") {
				t.Fatalf("message shows an internal error: %q", got.Message)
			}
		})
	}
}

// ---- crash at every effect boundary ----

// A crash is simulated the way the existing executor tests do: the proposal is
// left Confirmed (confirmWithoutRunning), the state the dead process would have
// left behind is arranged by hand, the service is closed and a new one starts
// on the same database (Recovered -> Reconcile, never Execute).

func TestCrashIgnoreAtEveryBoundary(t *testing.T) {
	t.Run("before anything started", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.ignoreAction("*.log\n", true))
		x.confirmWithoutRunning(v)
		x.restart()
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable || x.ign.Appends() != 0 {
			t.Fatalf("failure = %+v, appends %d", got, x.ign.Appends())
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitFor(v.ID, core.ProposalExecuted)
		if strings.Count(x.ignoreText(), "/dist/") != 1 {
			t.Fatalf(".gitignore = %q", x.ignoreText())
		}
	})
	t.Run("after the started record, before the write", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.ignoreAction("*.log\n", true))
		x.confirmWithoutRunning(v)
		p := proposalOf(t, x.world, v.ID)
		if err := x.svc.auditStage(x.ctx, p, 1, stageIgnoreStarted); err != nil {
			t.Fatal(err)
		}
		x.restart()
		x.waitFor(v.ID, core.ProposalFailed)
		if x.ign.Appends() != 0 || x.ignoreText() != "*.log\n" {
			t.Fatal("a restart wrote to .gitignore")
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitFor(v.ID, core.ProposalExecuted)
		if x.ign.Appends() != 1 {
			t.Fatalf("appends = %d", x.ign.Appends())
		}
	})
	t.Run("after the write landed, before the result was recorded", func(t *testing.T) {
		x := newP6(t)
		a := x.ignoreAction("*.log\n", true)
		v := x.create(a)
		x.confirmWithoutRunning(v)
		if _, _, err := x.ign.AppendIgnore(x.ctx, x.mainDir, a.Entries, platform.IgnoreExpectation{Hash: a.ExpectedHash}); err != nil {
			t.Fatal(err)
		}
		landed := x.ignoreText()
		x.restart()
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailInterrupted) {
			t.Fatalf("failure = %+v", got)
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		again := x.waitForAttempt(v.ID, 2)
		if again.FailKind != string(coreprop.FailStale) || x.ignoreText() != landed {
			t.Fatalf("retry = %+v; .gitignore %q, want %q", again, x.ignoreText(), landed)
		}
		if x.ign.Appends() != 1 {
			t.Fatalf("appends = %d: the entries were appended twice", x.ign.Appends())
		}
	})
}

func TestCrashCommentAtEveryBoundary(t *testing.T) {
	const body = "Looks fine."
	t.Run("before anything started", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.commentAction(body))
		x.confirmWithoutRunning(v)
		x.restart()
		x.waitFor(v.ID, core.ProposalFailed)
		if x.host.Posts() != 0 {
			t.Fatal("a restart posted")
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitFor(v.ID, core.ProposalExecuted)
		if len(x.host.Posted()) != 1 {
			t.Fatalf("comments = %d", len(x.host.Posted()))
		}
	})
	t.Run("after the intent record, before the POST", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.commentAction(body))
		x.confirmWithoutRunning(v)
		if err := x.svc.beginEffect(x.ctx, proposalOf(t, x.world, v.ID), 1, mrMarker, stageCommentStarted); err != nil {
			t.Fatal(err)
		}
		x.restart()
		x.waitFor(v.ID, core.ProposalFailed)
		if x.host.Posts() != 0 || x.host.Finds() == 0 {
			t.Fatalf("posts %d, finds %d: recovery must only read", x.host.Posts(), x.host.Finds())
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitFor(v.ID, core.ProposalExecuted)
		if len(x.host.Posted()) != 1 || x.host.Posts() != 1 {
			t.Fatalf("comments %d, posts %d", len(x.host.Posted()), x.host.Posts())
		}
	})
	t.Run("after the POST landed, before the effect was settled", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.commentAction(body))
		x.confirmWithoutRunning(v)
		if err := x.svc.beginEffect(x.ctx, proposalOf(t, x.world, v.ID), 1, mrMarker, stageCommentStarted); err != nil {
			t.Fatal(err)
		}
		if _, err := x.host.PostReviewComment(x.ctx, x.repo.ID, 7, mrHead, body, mrMarker); err != nil {
			t.Fatal(err)
		}
		x.restart()
		got := x.waitFor(v.ID, core.ProposalExecuted)
		if len(x.host.Posted()) != 1 || x.host.Posts() != 1 {
			t.Fatalf("comments %d, posts %d: the comment was posted twice", len(x.host.Posted()), x.host.Posts())
		}
		if es := x.effects(v.ID); len(es) != 1 || es[0] != "succeeded" || !strings.Contains(got.Result.Note, x.host.Posted()[0].ID) {
			t.Fatalf("effects %v, result %+v", es, got.Result)
		}
	})
	t.Run("after the effect was settled, before the proposal was", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.commentAction(body))
		x.confirmWithoutRunning(v)
		x.host.ScriptPost(proposalstest.NoFault, nil)
		p := proposalOf(t, x.world, v.ID)
		// Run the executor to its end without recording the proposal result.
		out := x.svc.execComment(x.ctx, p, p.Action.(coreprop.ReviewComment), 1)
		if !out.done {
			t.Fatalf("executor = %+v", out)
		}
		finds := x.host.Finds()
		x.restart()
		x.waitFor(v.ID, core.ProposalExecuted)
		if x.host.Posts() != 1 || x.host.Finds() != finds {
			t.Fatalf("posts %d, finds %d -> %d: the local record should have been enough", x.host.Posts(), finds, x.host.Finds())
		}
	})
	t.Run("a second run of the same attempt posts nothing", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.commentAction(body))
		x.confirmWithoutRunning(v)
		p := proposalOf(t, x.world, v.ID)
		if err := x.svc.beginEffect(x.ctx, p, 1, mrMarker, stageCommentStarted); err != nil {
			t.Fatal(err)
		}
		out := x.svc.execComment(x.ctx, p, p.Action.(coreprop.ReviewComment), 1)
		if out.done || x.host.Posts() != 0 {
			t.Fatalf("executor = %+v, posts %d", out, x.host.Posts())
		}
	})
}

func TestCrashPullAtEveryBoundary(t *testing.T) {
	t.Run("before anything started", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.pullAction())
		x.confirmWithoutRunning(v)
		x.restart()
		x.waitFor(v.ID, core.ProposalFailed)
		if len(x.pull.Calls()) != 0 {
			t.Fatal("a restart pulled")
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		x.waitFor(v.ID, core.ProposalExecuted)
		if len(x.pull.Calls()) != 1 {
			t.Fatalf("pulls = %d", len(x.pull.Calls()))
		}
	})
	t.Run("after the started record, before the pull", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.pullAction())
		x.confirmWithoutRunning(v)
		if err := x.svc.auditStage(x.ctx, proposalOf(t, x.world, v.ID), 1, stagePullStarted); err != nil {
			t.Fatal(err)
		}
		x.restart()
		x.waitFor(v.ID, core.ProposalFailed)
		if len(x.pull.Calls()) != 0 {
			t.Fatal("a restart pulled")
		}
	})
	t.Run("after the pull landed, before the result was recorded", func(t *testing.T) {
		x := newP6(t)
		v := x.create(x.pullAction())
		x.confirmWithoutRunning(v)
		x.git.Write(x.mainDir, "pulled.txt", "p\n") // the fast-forward moved the tip
		_ = x.git.Fake.Add(x.ctx, x.mainDir, []string{"pulled.txt"})
		if _, err := x.git.Fake.Commit(x.ctx, x.mainDir, []string{"pulled.txt"}, "upstream", ""); err != nil {
			t.Fatal(err)
		}
		x.restart()
		got := x.waitFor(v.ID, core.ProposalFailed)
		if got.FailKind != string(coreprop.FailInterrupted) {
			t.Fatalf("failure = %+v", got)
		}
		if _, err := x.svc.Retry(x.ctx, v.ID, "user"); err != nil {
			t.Fatal(err)
		}
		again := x.waitForAttempt(v.ID, 2)
		if again.FailKind != string(coreprop.FailStale) || len(x.pull.Calls()) != 0 {
			t.Fatalf("retry = %+v, pulls %d", again, len(x.pull.Calls()))
		}
	})
}

// ---- invariant 4: no token-like text in audit, view or events ----

func TestInvariant_4_Phase6PathsNeverShowATokenInAuditViewOrEvents(t *testing.T) {
	tokenErr := errors.New("request failed with token " + testToken)
	for name, run := range map[string]func(x *p6) core.ProposalID{
		"ignore": func(x *p6) core.ProposalID {
			v := x.create(x.ignoreAction("*.log\n", true))
			x.ign.Script(proposalstest.FailBefore, tokenErr)
			x.confirm(v)
			return v.ID
		},
		"comment unknown": func(x *p6) core.ProposalID {
			v := x.create(x.commentAction("Looks fine."))
			x.host.ScriptPost(proposalstest.FailBefore, tokenErr)
			x.host.ScriptFind(coreprop.CommentLookup{}, tokenErr)
			x.confirm(v)
			return v.ID
		},
		"comment head": func(x *p6) core.ProposalID {
			v := x.create(x.commentAction("Looks fine."))
			x.host.ScriptHead(tokenErr)
			x.confirm(v)
			return v.ID
		},
		"pull": func(x *p6) core.ProposalID {
			v := x.create(x.pullAction())
			x.pull.Script(proposalstest.FailBefore, tokenErr)
			x.confirm(v)
			return v.ID
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := newP6(t, testToken)
			id := run(x)
			x.waitFor(id, core.ProposalFailed)
			if text := x.visibleText(id); strings.Contains(text, testToken) {
				t.Fatalf("a token is visible:\n%s", text)
			}
		})
	}
}
