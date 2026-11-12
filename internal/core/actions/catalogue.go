package actions

import "github.com/brendatamaar/yggdrasil/internal/core"

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
	Kind   Kind
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
}

// Catalogue returns the typed offerings for the facts, in a stable order:
// by reason (the order of Reasons), primary kind first. Every reason in the
// facts yields at least one offering (TestPropEveryAttentionReasonHasAction).
//
// PENDING (task A): the foundation defines the contract (PrimaryKind, Mode,
// Offering) but returns no offerings, so a premature caller shows nothing
// instead of a wrong action. The orchestration task implements it.
func Catalogue(f Facts) []Offering {
	_ = f
	return nil
}
