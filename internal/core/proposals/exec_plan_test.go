package proposals

import (
	"math/rand"
	"testing"
)

func genComment(r *rand.Rand) ReviewComment {
	return ReviewComment{Repo: "rep_1", MRNumber: 1 + r.Intn(50), ExpectedHead: hexStr(r, 40), Body: "note " + hexStr(r, 8), Marker: hexStr(r, 16)}
}

func genStatuses(r *rand.Rand) []EffectStatus {
	n := r.Intn(5)
	out := make([]EffectStatus, n)
	for i := range out {
		out[i] = pick(r, EffectStarted, EffectDone, EffectRefused, EffectUncertain, EffectStatus("garbage"))
	}
	return out
}

// A POST is planned only for a head equal to the reviewed head and only when no
// earlier attempt may have posted (never a blind retry, AC-02).
func TestPropPlanCommentPostNeedsHeadAndCleanHistory(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		a := genComment(r)
		states := genStatuses(r)
		prior := PriorOfEffects(states)
		head := pick(r, a.ExpectedHead, hexStr(r, 40), "")
		plan := PlanComment(a, prior, head)
		if plan == PlanCommentPost {
			if head != a.ExpectedHead {
				t.Fatalf("post planned for head %q, reviewed %q", head, a.ExpectedHead)
			}
			for _, s := range states {
				if s != EffectRefused {
					t.Fatalf("post planned after earlier effect %q (%v)", s, states)
				}
			}
		}
		for _, s := range states {
			if s == EffectDone && plan != PlanCommentDone {
				t.Fatalf("an earlier posted comment gave %s", plan)
			}
		}
	})
}

func TestPlanCommentTable(t *testing.T) {
	a := ReviewComment{ExpectedHead: head40}
	cases := []struct {
		prior CommentPrior
		head  string
		want  CommentPlan
	}{
		{PriorNone, head40, PlanCommentPost},
		{PriorNotPosted, head40, PlanCommentPost},
		{PriorNone, hash64[:40], PlanCommentStale},
		{PriorNone, "", PlanCommentStale},
		{PriorMaybePosted, head40, PlanCommentLookup},
		{PriorMaybePosted, "", PlanCommentLookup},
		{PriorPosted, "", PlanCommentDone},
		{CommentPrior("x"), head40, PlanCommentLookup},
	}
	for _, c := range cases {
		if got := PlanComment(a, c.prior, c.head); got != c.want {
			t.Errorf("PlanComment(%s, %q) = %s, want %s", c.prior, c.head, got, c.want)
		}
	}
}

func TestPriorOfEffects(t *testing.T) {
	cases := []struct {
		in   []EffectStatus
		want CommentPrior
	}{
		{nil, PriorNone},
		{[]EffectStatus{EffectRefused}, PriorNotPosted},
		{[]EffectStatus{EffectRefused, EffectUncertain}, PriorMaybePosted},
		{[]EffectStatus{EffectUncertain, EffectRefused}, PriorMaybePosted},
		{[]EffectStatus{EffectStarted}, PriorMaybePosted},
		{[]EffectStatus{EffectUncertain, EffectDone}, PriorPosted},
		{[]EffectStatus{EffectStatus("garbage")}, PriorMaybePosted},
	}
	for _, c := range cases {
		if got := PriorOfEffects(c.in); got != c.want {
			t.Errorf("PriorOfEffects(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

// Only an exact, found comment is Posted; its receipt is complete and valid.
func TestPropJudgeCommentLookup(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		a := genComment(r)
		l := CommentLookup{
			Outcome:   pick(r, CommentFound, CommentAbsent, CommentUnknown, CommentConflict, CommentLookupOutcome("")),
			CommentID: pick(r, "", "1234", "bad id", "a/b"),
		}
		v, rec := JudgeCommentLookup(a, l)
		switch v {
		case CommentPosted:
			if l.Outcome != CommentFound {
				t.Fatalf("posted from %s", l.Outcome)
			}
			if err := rec.Validate(); err != nil {
				t.Fatalf("receipt of a posted verdict is invalid: %v", err)
			}
			if rec.Marker != a.Marker || rec.Head != a.ExpectedHead || rec.BodyHash != CommentBodyHash(a.Body) || rec.CommentID != l.CommentID {
				t.Fatalf("receipt %+v does not match the action", rec)
			}
		case CommentMayRetry:
			if l.Outcome != CommentAbsent {
				t.Fatalf("may-retry from %s", l.Outcome)
			}
		case CommentBlocked:
			if l.Outcome == CommentAbsent {
				t.Fatal("a complete read that found nothing was blocked")
			}
			if rec != (CommentReceipt{}) {
				t.Fatalf("blocked verdict has a receipt %+v", rec)
			}
		default:
			t.Fatalf("verdict %q", v)
		}
	})
}

func TestCommentBodyHashIsSHA256(t *testing.T) {
	// sha256("") is the well-known empty digest.
	if got := CommentBodyHash(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("CommentBodyHash(\"\") = %s", got)
	}
	if CommentBodyHash("a") == CommentBodyHash("b") {
		t.Error("different bodies share a hash")
	}
}

// A changed or missing .gitignore is never reported as Done: the writer's own
// refusal is the guard, and recovery cannot prove who changed the file.
func TestPropReconcileIgnoreNeverDone(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		a := IgnoreEntries{Where: MainCheckout{Repo: "rep_1"}, Entries: []string{"/dist/"}, ExpectedAbsent: r.Intn(2) == 0}
		if !a.ExpectedAbsent {
			a.ExpectedHash = hexStr(r, 64)
		}
		f := IgnoreFile{Absent: r.Intn(2) == 0}
		if !f.Absent {
			f.Hash = pick(r, a.ExpectedHash, hexStr(r, 64))
		}
		got := ReconcileIgnore(a, f)
		if got == ReconcileDone {
			t.Fatal("reconcile said done without reading the entries")
		}
		if (got == ReconcileNotDone) != IgnoreMatches(a, f) {
			t.Fatalf("not-done %v but matches %v", got == ReconcileNotDone, IgnoreMatches(a, f))
		}
	})
}

func TestIgnoreMatchesDistinguishesAbsentFromEmpty(t *testing.T) {
	empty := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	abs := IgnoreEntries{Where: MainCheckout{Repo: "rep_1"}, Entries: []string{"/dist/"}, ExpectedAbsent: true}
	has := IgnoreEntries{Where: MainCheckout{Repo: "rep_1"}, Entries: []string{"/dist/"}, ExpectedHash: empty}
	if IgnoreMatches(abs, IgnoreFile{Hash: empty}) {
		t.Error("an empty file matched an expected-absent action")
	}
	if IgnoreMatches(has, IgnoreFile{Absent: true}) {
		t.Error("an absent file matched the checksum of an empty one")
	}
	if !IgnoreMatches(abs, IgnoreFile{Absent: true}) || !IgnoreMatches(has, IgnoreFile{Hash: empty}) {
		t.Error("the expected state did not match")
	}
}

// A pull is planned only on the confirmed branch at the confirmed tip.
func TestPropPlanPullNeedsBranchAndTip(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		a := PullFF{Repo: "rep_1", Branch: "main", Upstream: "origin/main", ExpectHead: hexStr(r, 40)}
		branch := pick(r, "main", "dev", "")
		head := pick(r, a.ExpectHead, hexStr(r, 40), "")
		got := PlanPull(a, branch, head)
		if (got == PlanPullGo) != (branch == a.Branch && head == a.ExpectHead) {
			t.Fatalf("PlanPull(%q, %q) = %s", branch, head, got)
		}
		if rc := ReconcilePull(a, head); rc == ReconcileDone {
			t.Fatal("a pull was reconciled as done without proof")
		}
	})
}
