package actions

import (
	"slices"
	"sort"
	"strconv"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Capabilities are what the server can do for one repo right now. They are
// facts supplied by the shell, never assumptions: code checks them instead of
// guessing that a host or a token exists (docs/AGENTS.md 5.5 spirit).
type Capabilities struct {
	// HostLogs: failed-job logs can be read (linked instance, token, GitHub or
	// GitLab).
	HostLogs bool
	// HostIssues: issues can be read.
	HostIssues bool
	// HostMR: an MR diff and head can be read.
	HostMR bool
	// CommentPost: a review comment can be posted after confirmation.
	CommentPost bool
	// BuildLog: the last failed local build has a readable log.
	BuildLog bool
}

// Facts are the repo facts the catalogue is computed from.
type Facts struct {
	Repo core.RepoID
	// Readable: the working directory can be read. When false, a diagnosis
	// is still offered, but it only gets tracked metadata and is labelled as
	// waiting for a valid folder (AC-01 fallback; never a fabricated run).
	Readable bool
	Reasons  []Reason
	Caps     Capabilities
	// CIRunID and CIHead pin a failed pipeline; empty when none is known.
	CIRunID string
	CIHead  string
	// BuildID is the last failed local build; empty when none.
	BuildID string
	// MRNumber and MRHead pin an open merge request; zero/empty when none.
	MRNumber int
	MRHead   string
	// UntrackedCount lets the catalogue offer ignore suggestions at the noise
	// threshold; the candidates come from the noise analysis, not from here.
	UntrackedCount int
	// Head is the repo's current HEAD, which a stale offering is checked against.
	Head string
}

// ContextAvailability says whether an offering's source context can be read.
// An unavailable context is explained, and the action stays offered with the
// fallback.
type ContextAvailability string

const (
	ContextAvailable ContextAvailability = "available"
	// ContextMetadataOnly: the host or filesystem detail is missing; the Ask
	// task gets only the tracked facts and says so.
	ContextMetadataOnly ContextAvailability = "metadata_only"
	// ContextNone: the kind needs no source context.
	ContextNone ContextAvailability = "none"
)

// Offering is one entry of the catalogue.
type Offering struct {
	Kind Kind
	// Reason is the attention reason the offering answers; empty for an
	// offering that is not about one (an open MR, ignore suggestions).
	Reason Reason
	// Mode is Kind.Mode(), repeated so the UI can show it without a table.
	Mode    core.TaskMode
	Context ContextAvailability
	// SourceID pins the object the offering acts on ("" for kinds with none).
	SourceID string
	// WaitingForFolder: the repo cannot be read, so a started task would wait
	// for a restored working directory. The UI must not present it as running.
	WaitingForFolder bool
	// Note is a plain sentence explaining a degraded context, empty otherwise.
	Note string
	// OptionalVerify (additive field): the offering may run the fast
	// verification after a confirmed pull. Set only for PullAndCheck; the user
	// opts in on the preview, it is never implied.
	OptionalVerify bool
}

// UntrackedNoiseThreshold mirrors repos.NoiseMinUntracked: at or above this
// untracked count the catalogue offers ignore suggestions (WS-09). The core
// domain packages are leaves, so the value is repeated here.
const UntrackedNoiseThreshold = 100

// Catalogue returns the typed offerings for the facts, in a stable order: the
// attention reasons in the order of Reasons (the primary kind first, then a
// separate follow-up such as FixPipeline), then reason-less offerings (an open
// merge request, ignore suggestions). It is total: every reason in the facts
// yields at least one offering that starts an agent turn or a proposal, never
// one that only opens a detail page, and a missing token, host capability or
// unreadable folder only degrades the offering's context and says so
// (TestPropEveryAttentionReasonHasAction, AC-01 fallback, E6). Reasons this
// package does not know are given the safe repo diagnosis. Duplicate reasons
// collapse; no ranking or limit ever drops an offering.
func Catalogue(f Facts) []Offering {
	present := map[Reason]bool{}
	var unknown []Reason
	for _, r := range f.Reasons {
		if r.Validate() == nil {
			present[r] = true
		} else if !slices.Contains(unknown, r) {
			unknown = append(unknown, r)
		}
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })

	var out []Offering
	for _, r := range Reasons() {
		if present[r] {
			out = append(out, forReason(f, r)...)
		}
	}
	for _, r := range unknown {
		out = append(out, diagnoseRepo(f, r, "This problem isn't one the app knows yet, so the agent only gets the repo's tracked facts."))
	}
	if f.MRNumber > 0 && f.MRHead != "" {
		out = append(out, reviewMR(f))
	}
	if f.Readable && f.UntrackedCount >= UntrackedNoiseThreshold {
		r := Reason("")
		if present[ReasonDirty] {
			r = ReasonDirty
		}
		out = append(out, offer(f, ProposeIgnore, r, "", ContextAvailable,
			"The agent looks at a bounded sample of the untracked files and suggests .gitignore entries; you confirm the exact lines."))
	}
	return out
}

// Pin is the value a preview pins and a submit is checked against: the MR head
// for a review, the pipeline's commit for a pipeline action when it is known,
// the repo HEAD otherwise. The shell passes Pin(facts-now, kind) as
// SubmitRequested.ScopeHead.
func Pin(f Facts, k Kind) string {
	switch k {
	case ReviewMR:
		return f.MRHead
	case DiagnosePipeline, FixPipeline:
		if f.CIHead != "" {
			return f.CIHead
		}
	}
	return f.Head
}

const (
	noteFolder = "The folder can't be read right now. The task is saved as a draft and waits until the folder is restored; nothing runs on a folder that isn't there."
	noteRepo   = "the agent gets only tracked facts (branch, commits, names of conflicted files), and it will not reset, merge or delete anything."
)

func forReason(f Facts, r Reason) []Offering {
	switch r {
	case ReasonUnreadable:
		return []Offering{diagnoseRepo(f, r, "The folder can't be read. The agent gets only tracked metadata and the draft waits for a valid folder.")}
	case ReasonConflicts:
		return []Offering{diagnoseRepo(f, r, "Merge conflicts: "+noteRepo)}
	case ReasonDiverged:
		return []Offering{diagnoseRepo(f, r, "The branch has diverged from its upstream: "+noteRepo)}
	case ReasonFailedCI:
		if f.CIRunID == "" {
			return []Offering{diagnoseRepo(f, r, "The failed pipeline isn't identified, so the agent gets only the repo's tracked facts and no job log.")}
		}
		ctx, note := ContextAvailable, ""
		if !f.Caps.HostLogs {
			ctx, note = ContextMetadataOnly, "The job log isn't available (no linked host or token). The agent gets only the pipeline's name, status and commit."
		}
		return []Offering{
			offer(f, DiagnosePipeline, r, f.CIRunID, ctx, note),
			offer(f, FixPipeline, r, f.CIRunID, ctx, joinNote("A separate Code task in its own worktree; it never changes the diagnosis task.", note)),
		}
	case ReasonFailedBuild:
		if f.BuildID == "" {
			return []Offering{diagnoseRepo(f, r, "The failed build isn't identified, so the agent gets only the repo's tracked facts and no build log.")}
		}
		if !f.Caps.BuildLog {
			return []Offering{offer(f, DiagnoseBuild, r, f.BuildID, ContextMetadataOnly,
				"The build log can't be read. The agent gets only the build's command and result.")}
		}
		return []Offering{offer(f, DiagnoseBuild, r, f.BuildID, ContextAvailable, "")}
	case ReasonBehind:
		o := offer(f, PullAndCheck, r, "", ContextNone,
			"A fast-forward pull you confirm. If the repo has local changes, has diverged or is locked, it says why and changes nothing.")
		o.OptionalVerify = true
		return []Offering{o}
	case ReasonDirty:
		ctx := ContextAvailable
		if !f.Readable {
			ctx = ContextMetadataOnly
		}
		return []Offering{offer(f, ProposeCommits, r, "", ctx,
			"The agent groups the current diff by exact paths and drafts one commit proposal per group; nothing is staged or committed until you confirm.")}
	}
	return nil
}

func diagnoseRepo(f Facts, r Reason, note string) Offering {
	return offer(f, DiagnoseRepo, r, "", ContextMetadataOnly, note)
}

func reviewMR(f Facts) Offering {
	ctx, note := ContextAvailable, "The agent reads the pinned diff and drafts comments as proposals; nothing is posted until you confirm each one."
	if !f.Caps.HostMR {
		ctx, note = ContextMetadataOnly, "The merge request's diff can't be read (no linked host or token). The agent gets only its title and head."
	}
	if !f.Caps.CommentPost {
		note = joinNote(note, "Posting comments isn't available for this host, so the drafts can only be copied.")
	}
	return offer(f, ReviewMR, "", strconv.Itoa(f.MRNumber), ctx, note)
}

// offer builds one offering and applies the folder rule: every kind except a
// draft import needs a working directory for its task, so an unreadable repo
// marks it as waiting for the folder.
func offer(f Facts, k Kind, r Reason, source string, ctx ContextAvailability, note string) Offering {
	o := Offering{Kind: k, Reason: r, Mode: k.Mode(), Context: ctx, SourceID: source, Note: note}
	if !f.Readable && k != ImportIssue {
		o.WaitingForFolder = true
		o.Note = joinNote(note, noteFolder)
	}
	return o
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " " + b
}
