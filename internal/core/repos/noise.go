package repos

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

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
//
// The sample is not a random draw: git lists untracked files in path order and
// the reader keeps the first ones, so a truncated sample is the alphabetical
// prefix of the untracked files. Truncated is the explicit marker for that. It
// is also set whenever the sample holds fewer paths than UntrackedCount (see
// IsPartial), and every candidate derived from such facts carries
// Incomplete, so a UI can say "based on a partial sample".
type NoiseFacts struct {
	Repo           core.RepoID
	UntrackedCount int
	Sample         []string
	Truncated      bool
}

// IsPartial reports whether the sample is only a part of the untracked files:
// the reader truncated, or fewer paths are present than are counted. A partial
// sample is always an alphabetical prefix, so the "at least half of the sample"
// rule measures that prefix and not the whole repo.
func (f NoiseFacts) IsPartial() bool {
	return f.Truncated || len(f.Sample) < f.UntrackedCount
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
// example "/web/node_modules/". Incomplete is true whenever the sample is only
// a prefix of the untracked files (NoiseFacts.IsPartial): the suggestion is
// still shown, labelled as based on a partial sample. A candidate is never
// nested under another candidate.
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
// proposal creation; Proposal is the ID the shell generated. No time field:
// nothing in the step reads one, the proposal's own creation time is the
// shell's.
type NoiseSelected struct {
	Entries     []string
	CurrentHash string
	Absent      bool
	RequestID   core.RequestID
	Proposal    core.ProposalID
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
	if len(f.Sample) > 5000 {
		return fmt.Errorf("noise sample exceeds status limit")
	}
	seen := map[string]bool{}
	for _, p := range f.Sample {
		if !ValidNoisePath(p) || seen[p] {
			return fmt.Errorf("invalid or duplicate noise path")
		}
		seen[p] = true
	}
	return nil
}

// NoiseStep implements the approved analysis and selection transitions. It never
// writes files or executes proposals.
func NoiseStep(n Noise, in NoiseInput) (Noise, []NoiseCommand) {
	switch x := in.(type) {
	case NoiseScopeRemoved:
		n.State = NoSuggestion{}
	case NoiseObserved:
		switch n.State.(type) {
		case NoSuggestion, Suggested:
			if x.Facts.Repo != n.Repo || x.Facts.Validate() != nil {
				return n, nil
			}
			n.State = classifyNoise(x.Facts)
		case AwaitingConfirmation:
		}
	case NoiseSelected:
		s, ok := n.State.(Suggested)
		if !ok || !validNoiseSelection(s, x) {
			return n, nil
		}
		n.State = AwaitingConfirmation{Proposal: x.Proposal}
		entries := slices.Clone(x.Entries)
		slices.Sort(entries)
		return n, []NoiseCommand{NoiseCreateIgnoreProposal{Proposal: x.Proposal, RequestID: x.RequestID, Entries: entries, ExpectHash: x.CurrentHash, ExpectAbsent: x.Absent}}
	case NoiseProposalDecided:
		s, ok := n.State.(AwaitingConfirmation)
		if ok && s.Proposal == x.Proposal && (x.Status == core.ProposalExecuted || x.Status == core.ProposalRejected || x.Status == core.ProposalFailed) {
			n.State = NoSuggestion{}
			return n, []NoiseCommand{NoiseRefreshStatus{Repo: n.Repo}}
		}
	}
	return n, nil
}

// RunNoise folds inputs without performing effects.
func RunNoise(n Noise, inputs []NoiseInput) (Noise, []NoiseCommand) {
	var commands []NoiseCommand
	for _, in := range inputs {
		next, cs := NoiseStep(n, in)
		n = next
		commands = append(commands, cs...)
	}
	return n, commands
}

// ValidNoisePath accepts exact slash-separated file paths, never pathspecs,
// traversal, Windows ADS, control characters or ambiguous Windows components.
func ValidNoisePath(p string) bool {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.ContainsAny(p, `\:`) {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func classifyNoise(f NoiseFacts) NoiseState {
	if f.UntrackedCount < NoiseMinUntracked || len(f.Sample) < NoiseMinSample {
		return NoSuggestion{}
	}
	counts := map[string]int{}
	for _, file := range f.Sample {
		parts := strings.Split(file, "/")
		end := 0
		for _, part := range parts[:len(parts)-1] {
			end += len(part)
			if slices.Contains(NoiseDirs(), part) {
				// Slice the immutable path instead of allocating every prefix.
				counts[file[:end]]++
			}
			end++
		}
	}
	evidence := NoiseEvidence{Total: f.UntrackedCount, Sampled: len(f.Sample)}
	var candidates []NoiseCandidate
	for dir, count := range counts {
		if count < NoiseMinSample || count < (len(f.Sample)+1)/2 {
			continue
		}
		pattern := "/" + strings.NewReplacer(`\`, `\\`, "*", `\*`, "?", `\?`, "[", `\[`, "]", `\]`, "!", `\!`, "#", `\#`, " ", `\ `).Replace(dir) + "/"
		e := evidence
		e.UnderDirectory = count
		candidates = append(candidates, NoiseCandidate{Directory: dir, SampleCount: count, Pattern: pattern, Evidence: e, Incomplete: f.IsPartial()})
	}
	candidates = dropNested(candidates)
	slices.SortFunc(candidates, func(a, b NoiseCandidate) int { return strings.Compare(a.Directory, b.Directory) })
	if len(candidates) == 0 {
		return NoSuggestion{}
	}
	return Suggested{Candidates: candidates, Evidence: evidence}
}

// dropNested removes every candidate that lies under another candidate, so
// "/web/node_modules/" is offered without "/web/node_modules/x/build/". The
// outer directory always qualifies when an inner one does (it holds at least
// as many sampled files), so no covered path is lost. Nesting is by whole path
// segment: "bin" does not contain "binX/obj". Directories come from validated
// paths, which cannot hold a backslash or a line break.
func dropNested(cs []NoiseCandidate) []NoiseCandidate {
	kept := cs[:0:0]
	for _, c := range cs {
		if !slices.ContainsFunc(cs, func(o NoiseCandidate) bool { return nestedUnder(c.Directory, o.Directory) }) {
			kept = append(kept, c)
		}
	}
	return kept
}

// nestedUnder reports whether dir is strictly inside outer, by path segment.
func nestedUnder(dir, outer string) bool {
	return len(dir) > len(outer) && dir[len(outer)] == '/' && strings.HasPrefix(dir, outer)
}

func validNoiseSelection(s Suggested, x NoiseSelected) bool {
	if x.Proposal.Validate() != nil || x.RequestID.Validate() != nil || len(x.Entries) == 0 || len(x.Entries) > NoiseMaxEntries {
		return false
	}
	if x.Absent {
		if x.CurrentHash != "" {
			return false
		}
	} else {
		if len(x.CurrentHash) != 64 {
			return false
		}
		for _, c := range x.CurrentHash {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	seen := map[string]bool{}
	size := 0
	for _, entry := range x.Entries {
		if seen[entry] {
			return false
		}
		seen[entry] = true
		size += len(entry) + 1
		if size > NoiseMaxBytes || !slices.ContainsFunc(s.Candidates, func(c NoiseCandidate) bool { return c.Pattern == entry }) {
			return false
		}
	}
	return true
}
