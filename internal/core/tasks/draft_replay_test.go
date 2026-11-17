package tasks

import (
	"math/rand"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// legacyImport is the decision the shell made before the replay moved into
// PlanDraft, transliterated from the old ImportIssueDraft: it is the oracle
// the core must agree with. stored is the committed link of the request ID (nil
// when there is none); existing is read only for a first request in scope.
func legacyImport(f DraftFacts, stored *Replayed) (kind string, reason DraftRefusal, task core.TaskID) {
	// The old shell refused through PlanDraft (scope, then valid facts) in both
	// the replay and the first-request path.
	switch {
	case !f.RepoTracked:
		return "refuse", DraftRepoUntracked, ""
	case !f.RepoReady:
		return "refuse", DraftRepoNotReady, ""
	case f.Now.IsZero() || f.TaskID.Validate() != nil:
		return "refuse", DraftBadFacts, ""
	}
	if stored != nil {
		if stored.Digest != f.Digest {
			return "mismatch", "", ""
		}
		return "replay", "", stored.Task
	}
	if f.Existing != "" {
		return "exists", "", f.Existing
	}
	return "create", "", ""
}

func randReplayFacts(r *rand.Rand) (DraftFacts, *Replayed) {
	f := randFacts(r)
	f.Digest = []string{"d1", "d2"}[r.Intn(2)]
	if r.Intn(2) == 0 {
		return f, nil
	}
	st := &Replayed{Task: core.TaskID([]string{"tsk_first", "tsk_other"}[r.Intn(2)]), Digest: []string{"d1", "d2"}[r.Intn(2)]}
	f.Replay = *st
	f.Existing = "" // the shell does not read the dedup key for a replay
	return f, st
}

// Invariant "the core decides the replay": over random sequences of requests,
// PlanDraft agrees with the decision the shell used to make, scope still comes
// before replay and digest, and a replay or a refusal never creates.
func TestPropPlanDraftReplayMatchesLegacyShell(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		for i := 0; i < 80; i++ {
			spec, err := ParseDraftSpec(randValidSpec(r))
			if err != nil {
				t.Fatal(err)
			}
			f, stored := randReplayFacts(r)
			kind, reason, task := legacyImport(f, stored)
			switch v := PlanDraft(spec, f).(type) {
			case DraftCreate:
				if kind != "create" {
					t.Fatalf("facts %+v stored %v: core creates, legacy %s", f, stored, kind)
				}
			case DraftExists:
				if kind != "exists" || v.Task != task {
					t.Fatalf("facts %+v: core exists %s, legacy %s %s", f, v.Task, kind, task)
				}
			case DraftReplayed:
				if kind != "replay" || v.Task != task {
					t.Fatalf("facts %+v stored %v: core replays %s, legacy %s %s", f, stored, v.Task, kind, task)
				}
			case DraftRefuse:
				switch {
				case v.Reason == DraftPayloadChanged && kind == "mismatch":
				case kind == "refuse" && v.Reason == reason:
				default:
					t.Fatalf("facts %+v stored %v: core refuses %s, legacy %s %s", f, stored, v.Reason, kind, reason)
				}
				if v.Reason.Message() == "" {
					t.Fatal("refusal without a message")
				}
			default:
				t.Fatalf("unknown outcome %T", v)
			}
		}
	})
}

func replayFacts(replay DraftReplay) DraftFacts {
	return DraftFacts{RepoTracked: true, RepoReady: true, Replay: replay, Digest: "d1", TaskID: "tsk_new", Now: epoch}
}

func TestPlanDraftNoReplayCreatesOrDedups(t *testing.T) {
	for _, replay := range []DraftReplay{nil, NoReplay{}} {
		f := replayFacts(replay)
		if _, ok := PlanDraft(validDraft(), f).(DraftCreate); !ok {
			t.Fatalf("replay %#v: want a new draft", replay)
		}
		f.Existing = "tsk_old"
		if got := PlanDraft(validDraft(), f); got != (DraftExists{Task: "tsk_old"}) {
			t.Fatalf("replay %#v: %#v", replay, got)
		}
	}
}

func TestPlanDraftReplayedSamePayloadReturnsStoredDraft(t *testing.T) {
	f := replayFacts(Replayed{Task: "tsk_first", Digest: "d1"})
	if got := PlanDraft(validDraft(), f); got != (DraftReplayed{Task: "tsk_first"}) {
		t.Fatalf("got %#v", got)
	}
}

func TestPlanDraftReplayedChangedPayloadIsRefused(t *testing.T) {
	f := replayFacts(Replayed{Task: "tsk_first", Digest: "other"})
	if got := PlanDraft(validDraft(), f); got != (DraftRefuse{Reason: DraftPayloadChanged}) {
		t.Fatalf("got %#v", got)
	}
}

func TestPlanDraftReplayedWithoutValidTaskIsBadFacts(t *testing.T) {
	f := replayFacts(Replayed{Task: "", Digest: "d1"})
	if got := PlanDraft(validDraft(), f); got != (DraftRefuse{Reason: DraftBadFacts}) {
		t.Fatalf("got %#v", got)
	}
}

// Scope precedes replay and digest (commit 2ce20e9): outside the project a
// request ID reveals neither its draft nor that it exists, whatever its digest.
func TestPlanDraftScopeComesBeforeReplayAndDigest(t *testing.T) {
	for _, digest := range []string{"d1", "other"} {
		f := replayFacts(Replayed{Task: "tsk_first", Digest: digest})
		f.RepoTracked = false
		if got := PlanDraft(validDraft(), f); got != (DraftRefuse{Reason: DraftRepoUntracked}) {
			t.Fatalf("untracked, digest %s: %#v", digest, got)
		}
		f.RepoTracked, f.RepoReady = true, false
		if got := PlanDraft(validDraft(), f); got != (DraftRefuse{Reason: DraftRepoNotReady}) {
			t.Fatalf("reserved, digest %s: %#v", digest, got)
		}
	}
}

func TestDraftPayloadChangedMessageIsPlain(t *testing.T) {
	if DraftPayloadChanged.Message() == DraftBadFacts.Message() || DraftPayloadChanged.Message() == "" {
		t.Fatal("the changed-payload refusal needs its own plain message")
	}
}
