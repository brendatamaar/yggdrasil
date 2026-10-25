package proposals

import (
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestParseActionRefusesUnavailableKinds(t *testing.T) {
	for _, k := range []string{"publish", "subtasks", "", "force_push", "reset"} {
		if _, err := ParseAction(k, []byte(`{}`)); !errors.Is(err, ErrUnsupportedKind) {
			t.Errorf("ParseAction(%q) = %v, want ErrUnsupportedKind", k, err)
		}
		if _, err := ActionFromRow(core.ProposalKind(k), []byte(`{}`)); !errors.Is(err, ErrUnsupportedKind) {
			t.Errorf("ActionFromRow(%q) = %v, want ErrUnsupportedKind", k, err)
		}
	}
	// A supported kind is not refused for its kind (the argument parser is a stub).
	if _, err := ParseAction("commit", []byte(`{}`)); errors.Is(err, ErrUnsupportedKind) {
		t.Error("commit refused as unsupported")
	}
}

func TestActionKinds(t *testing.T) {
	want := map[core.ProposalKind]Action{
		core.ProposalCommit: Commit{}, core.ProposalPush: Push{}, core.ProposalMergeRequest: MergeRequest{},
		core.ProposalAgentFile: AgentFile{}, core.ProposalDeleteFiles: DeleteFiles{}, core.ProposalInstall: Install{},
	}
	for k, a := range want {
		if a.Kind() != k {
			t.Errorf("%T.Kind() = %q, want %q", a, a.Kind(), k)
		}
		if err := supported(k); err != nil {
			t.Errorf("supported(%q) = %v", k, err)
		}
	}
}

func TestStepStubChangesNothing(t *testing.T) {
	p := Proposal{ID: "p1", Action: Commit{}, State: Pending{}}
	got, cmds := Step(p, Confirm{Hash: "x"})
	if got.State != p.State || cmds != nil {
		t.Fatal("Step stub changed something")
	}
}
