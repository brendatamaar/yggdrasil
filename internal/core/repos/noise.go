package repos

import (
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Noise detection (WS-09, docs/design/phase-6-noise.md): a pure analysis of the
// bounded untracked-file sample of an existing status read, which suggests
// exact .gitignore additions for generated directories. Analysis never writes;
// a confirmed core/proposals IgnoreEntries action does.

// Defaults of the classifier. Product tuning may change them through reviewed
// config, not silently.
const (
	// NoiseMinUntracked is the total untracked count at which noise is looked for.
	NoiseMinUntracked = 100
	// NoiseMinSample is the number of sampled files that must lie under one
	// candidate directory.
	NoiseMinSample = 50
	// NoiseMaxEntries and NoiseMaxBytes bound what one selection may add.
	NoiseMaxEntries = 32
	NoiseMaxBytes   = 4 << 10
)

// NoiseDirs is the allowlist of generated or dependency directory names a
// candidate may be. Anything else is never suggested: not a source directory,
// not a wildcard, not a single unknown file.
func NoiseDirs() []string {
	return []string{"node_modules", ".venv", "dist", "build", "target", "bin", "obj", "__pycache__"}
}

// NoiseFacts are the facts derived from a status read. Sample holds
// repo-relative forward-slash paths of untracked files, bounded by the status
// reader; Truncated says the reader stopped early, so the counts below
// the sample are lower bounds and no scan is claimed complete.
type NoiseFacts struct {
	Repo           core.RepoID
	UntrackedCount int
	Sample         []string
	Truncated      bool
}

// NoiseEvidence says what a candidate was derived from, for the UI to label.
type NoiseEvidence struct {
	// Total is the repo's untracked count; Sampled the sample size;
	// UnderDirectory how many sampled files lie under the candidate.
	Total          int
	Sampled        int
	UnderDirectory int
}

// NoiseCandidate is one suggested ignore entry. Directory is repo-relative;
// Pattern is the exact anchored gitignore line (metacharacters escaped), for
// example "/web/node_modules/". Incomplete is true when the sample was
// truncated: the suggestion is still shown, labelled as based on a sample.
type NoiseCandidate struct {
	Directory   string
	SampleCount int
	Pattern     string
	Evidence    NoiseEvidence
	Incomplete  bool
}

// NoiseState is the analysis state of one repo (sealed): NoSuggestion,
// Suggested or AwaitingConfirmation.
//
//sumtype:decl
type NoiseState interface{ isNoiseState() }

// NoSuggestion: nothing reaches the threshold.
type NoSuggestion struct{}

// Suggested: Candidates are offered; none is chosen yet.
type Suggested struct {
	Candidates []NoiseCandidate
	Evidence   NoiseEvidence
}

// AwaitingConfirmation: an ignore proposal was created and waits for the user.
type AwaitingConfirmation struct{ Proposal core.ProposalID }

func (NoSuggestion) isNoiseState()         {}
func (Suggested) isNoiseState()            {}
func (AwaitingConfirmation) isNoiseState() {}

// NoiseInput is one event for NoiseStep (sealed).
//
//sumtype:decl
type NoiseInput interface{ isNoiseInput() }

// NoiseObserved: a new status read produced facts.
type NoiseObserved struct{ Facts NoiseFacts }

// NoiseSelected: the user chose Entries among the current candidates. The shell
// read the current .gitignore: CurrentHash is its hash and Absent says it does
// not exist (an absent file is not an empty file). RequestID keys the
// proposal creation; Proposal is the ID the shell generated.
type NoiseSelected struct {
	Entries     []string
	CurrentHash string
	Absent      bool
	RequestID   core.RequestID
	Proposal    core.ProposalID
	Now         time.Time
}

// NoiseProposalDecided: the proposal reached a final status.
type NoiseProposalDecided struct {
	Proposal core.ProposalID
	Status   core.ProposalStatus
}

// NoiseScopeRemoved: the repo was untracked.
type NoiseScopeRemoved struct{}

func (NoiseObserved) isNoiseInput()        {}
func (NoiseSelected) isNoiseInput()        {}
func (NoiseProposalDecided) isNoiseInput() {}
func (NoiseScopeRemoved) isNoiseInput()    {}

// NoiseCommand is plain data asking the shell for an effect (sealed).
//
//sumtype:decl
type NoiseCommand interface{ isNoiseCommand() }

// NoiseCreateIgnoreProposal asks for one IgnoreEntries proposal, keyed by
// RequestID so a repeat finds the first. It writes nothing itself.
type NoiseCreateIgnoreProposal struct {
	Proposal     core.ProposalID
	RequestID    core.RequestID
	Entries      []string
	ExpectHash   string
	ExpectAbsent bool
}

// NoiseRefreshStatus asks for a status read after a decided proposal.
type NoiseRefreshStatus struct{ Repo core.RepoID }

func (NoiseCreateIgnoreProposal) isNoiseCommand() {}
func (NoiseRefreshStatus) isNoiseCommand()        {}

// Noise pairs the state with the repo it belongs to.
type Noise struct {
	Repo  core.RepoID
	State NoiseState
}

// Validate checks that the facts are internally consistent.
func (f NoiseFacts) Validate() error {
	if err := f.Repo.Validate(); err != nil {
		return err
	}
	if f.UntrackedCount < 0 || len(f.Sample) > f.UntrackedCount && !f.Truncated {
		return fmt.Errorf("noise facts: the sample is larger than the untracked count")
	}
	return nil
}

// NoiseStep is (noise2, commands) = NoiseStep(noise1, input) following
// docs/design/phase-6-noise.md.
//
// PENDING (task R): the foundation freezes the types. Every input returns the
// state unchanged and no commands, so nothing is suggested and no proposal is
// created until the classifier lands; it must never report a complete scan.
func NoiseStep(n Noise, in NoiseInput) (Noise, []NoiseCommand) {
	switch in.(type) {
	case NoiseObserved, NoiseSelected, NoiseProposalDecided, NoiseScopeRemoved:
		return n, nil
	}
	return n, nil
}
