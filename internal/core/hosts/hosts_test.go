package hosts

import "testing"

// Every variant of every closed set implements its sealed interface.
var (
	_ = []Key{RepoKey{}, InboxKey{}}
	_ = []Phase{Idle{}, Fetching{}, Waiting{}, Blocked{}}
	_ = []Payload{RepoState{}, Inbox{}}
	_ = []Input{ConfigChanged{}, TargetsChanged{}, Tick{}, RefreshRequested{}, Fetched{}, NotModified{}, FetchFailed{}}
	_ = []Command{Fetch{}, PublishKey{}, PublishHosts{}}
)

// Keys are map keys, so they must be comparable and distinct.
func TestKeysAreMapKeys(t *testing.T) {
	m := map[Key]int{
		RepoKey{Repo: "r1", Branch: "main"}: 1,
		RepoKey{Repo: "r1", Branch: "dev"}:  2,
		InboxKey{Instance: "github.com"}:    3,
	}
	if m[RepoKey{Repo: "r1", Branch: "main"}] != 1 || m[InboxKey{Instance: "github.com"}] != 3 || len(m) != 3 {
		t.Fatalf("keys collide: %v", m)
	}
}

func TestEnumsAcceptEveryConstantAndRejectOthers(t *testing.T) {
	type enum interface{ Validate() error }
	good := []enum{
		KindGitLab, KindGitHub, KindGitea,
		WaitTransient, WaitRateLimited,
		BlockUnauthorized, BlockNotFound, BlockUnsupported,
		MROpen, MRDraft, MRMerged, MRClosed,
		PipelinePending, PipelineRunning, PipelineSuccess, PipelineFailed, PipelineCanceled, PipelineSkipped, PipelineUnknown,
		RoleReviewRequested, RoleAssigned, RoleAuthored,
		FailTransient, FailRateLimited, FailUnauthorized, FailNotFound,
	}
	for _, e := range good {
		if err := e.Validate(); err != nil {
			t.Errorf("%T %q rejected: %v", e, e, err)
		}
	}
	bad := []enum{
		Kind(""), Kind("bitbucket"), WaitReason(""), WaitReason("slow"), BlockReason(""), BlockReason("forbidden"),
		MRState(""), MRState("Open"), PipelineStatus(""), PipelineStatus("passed"), Role(""), Role("owner"),
		FailKind(""), FailKind("timeout"),
	}
	for _, e := range bad {
		if e.Validate() == nil {
			t.Errorf("%T %q accepted", e, e)
		}
	}
}

func TestInstanceID(t *testing.T) {
	for _, s := range []string{"github.com", "git.acme.dev", "git.acme.dev:8443", "localhost:3000"} {
		if err := InstanceID(s).Validate(); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	for _, s := range []string{"", "GitHub.com", "git acme", "a/b", "u@host", "a\x00", "a\\b"} {
		if InstanceID(s).Validate() == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

// The placeholders change nothing until batch B3 lands the logic.
func TestPlaceholdersAreInert(t *testing.T) {
	p := Poller{}
	got, cmds := Run(p, []Input{Tick{}, RefreshRequested{}})
	if len(cmds) != 0 || len(got.Entries) != 0 {
		t.Fatalf("Run = %#v, %v", got, cmds)
	}
	if len(FailedPipelines(p)) != 0 || (Links(Instance{}, "a/b", "main") != LinkSet{}) {
		t.Fatal("placeholder returned data")
	}
}
