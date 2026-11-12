package actions

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Kind is a typed action of the catalogue. It is a closed set; the server
// builds the catalogue, so the browser never invents a kind.
type Kind string

const (
	DiagnosePipeline Kind = "diagnose_pipeline"
	FixPipeline      Kind = "fix_pipeline"
	ReviewMR         Kind = "review_mr"
	ProposeCommits   Kind = "propose_commits"
	PullAndCheck     Kind = "pull_and_check"
	ImportIssue      Kind = "import_issue"
	DiagnoseBuild    Kind = "diagnose_build"
	DiagnoseRepo     Kind = "diagnose_repo"
	ProposeIgnore    Kind = "propose_ignore"
)

// Kinds lists every kind in a stable order.
func Kinds() []Kind {
	return []Kind{DiagnosePipeline, FixPipeline, ReviewMR, ProposeCommits, PullAndCheck,
		ImportIssue, DiagnoseBuild, DiagnoseRepo, ProposeIgnore}
}

// Validate reports whether k is a known kind.
func (k Kind) Validate() error {
	for _, x := range Kinds() {
		if k == x {
			return nil
		}
	}
	return fmt.Errorf("unknown action kind %q", string(k))
}

// Mode is the task mode a kind runs in once submitted, or the empty mode for a
// kind that starts no agent turn (a draft import, a proposal-only pull).
// Diagnose and review kinds are Ask; only FixPipeline asks for Code, and only
// as a NEW task, never by re-moding an existing one (AC-03, invariant 2).
func (k Kind) Mode() core.TaskMode {
	switch k {
	case FixPipeline:
		return core.ModeCode
	case DiagnosePipeline, ReviewMR, ProposeCommits, DiagnoseBuild, DiagnoseRepo, ProposeIgnore:
		return core.ModeAsk
	}
	return "" // ImportIssue (draft only), PullAndCheck (a proposal)
}

// Reason is one reason a repo is in "Needs attention". The server derives
// them; the seven families are the ones the dashboard ranks today.
type Reason string

const (
	ReasonUnreadable  Reason = "unreadable"
	ReasonConflicts   Reason = "conflicts"
	ReasonDiverged    Reason = "diverged"
	ReasonFailedCI    Reason = "failed_ci"
	ReasonFailedBuild Reason = "failed_build"
	ReasonBehind      Reason = "behind"
	ReasonDirty       Reason = "dirty"
)

// Reasons lists every reason in a stable order.
func Reasons() []Reason {
	return []Reason{ReasonUnreadable, ReasonConflicts, ReasonDiverged, ReasonFailedCI,
		ReasonFailedBuild, ReasonBehind, ReasonDirty}
}

// Validate reports whether r is a known reason.
func (r Reason) Validate() error {
	for _, x := range Reasons() {
		if r == x {
			return nil
		}
	}
	return fmt.Errorf("unknown attention reason %q", string(r))
}

// PrimaryKind is the action offered first for a reason. Every reason has one
// (E6: no stranded reason), and it needs only facts the server always has: a
// missing token or an unreadable folder degrades its context, never removes
// the action.
func PrimaryKind(r Reason) (Kind, bool) {
	switch r {
	case ReasonUnreadable, ReasonConflicts, ReasonDiverged:
		return DiagnoseRepo, true
	case ReasonFailedCI:
		return DiagnosePipeline, true
	case ReasonFailedBuild:
		return DiagnoseBuild, true
	case ReasonBehind:
		return PullAndCheck, true
	case ReasonDirty:
		return ProposeCommits, true
	}
	return "", false
}

// Limits of the boundary.
const (
	MaxSourceIDLen = 64
	MaxAgentLen    = 64
	MaxModelLen    = 100
	// MaxContextBytes bounds the context attached to one prompt.
	MaxContextBytes = 64 << 10
)

// Request is one explicit submission, as parsed at the boundary. The browser
// generates RequestID once per submission, so a retry or a double click is the
// same request. SourceID names the source object (CI run ID, MR number, issue
// number, build ID) as the server resolved it; it is never a URL or a path.
type Request struct {
	RequestID core.RequestID
	RepoID    core.RepoID
	Kind      Kind
	SourceID  string
	Agent     string
	Model     string
}

// ErrInvalidRequest wraps every rejection by ParseRequest.
var ErrInvalidRequest = errors.New("invalid action request")

func invalid(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(f, a...))
}

// needsSource reports whether a kind is about one source object.
func needsSource(k Kind) bool {
	switch k {
	case DiagnosePipeline, FixPipeline, ReviewMR, ImportIssue, DiagnoseBuild:
		return true
	}
	return false
}

func idChars(s string) bool {
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}

// ParseRequest validates the fields of a submission and returns a Request
// whose Validate holds. Unknown kinds, a source ID on a kind that has none (or
// missing on one that needs it), a URL or path in SourceID, an unusable agent
// or model name and oversized fields are rejected.
func ParseRequest(requestID, repoID, kind, sourceID, agent, model string) (Request, error) {
	r := Request{
		RequestID: core.RequestID(requestID), RepoID: core.RepoID(repoID), Kind: Kind(kind),
		SourceID: sourceID, Agent: agent, Model: model,
	}
	if err := r.Validate(); err != nil {
		return Request{}, err
	}
	return r, nil
}

// Validate re-checks every rule of ParseRequest.
func (r Request) Validate() error {
	if err := r.RequestID.Validate(); err != nil {
		return invalid("the request id is not valid")
	}
	if len(r.RequestID) > 100 {
		return invalid("the request id is too long")
	}
	if err := r.RepoID.Validate(); err != nil {
		return invalid("the repo id is not valid")
	}
	if err := r.Kind.Validate(); err != nil {
		return invalid("unknown action")
	}
	switch {
	case len(r.SourceID) > MaxSourceIDLen || !idChars(r.SourceID):
		return invalid("the source is not a plain id")
	case needsSource(r.Kind) && r.SourceID == "":
		return invalid("%s needs a source", r.Kind)
	case !needsSource(r.Kind) && r.SourceID != "":
		return invalid("%s takes no source", r.Kind)
	}
	if r.Kind.Mode() != "" || r.Kind == ImportIssue {
		// An agent name is needed when a task will exist (draft or run).
		if r.Agent == "" || len(r.Agent) > MaxAgentLen || !idChars(r.Agent) {
			return invalid("choose an agent")
		}
	} else if r.Agent != "" && (len(r.Agent) > MaxAgentLen || !idChars(r.Agent)) {
		return invalid("the agent name is not valid")
	}
	if len(r.Model) > MaxModelLen || !utf8.ValidString(r.Model) || strings.ContainsFunc(r.Model, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		return invalid("the model name is not valid")
	}
	return nil
}

// Digest is what the server stores next to the RequestID: sha256 over the
// length-prefixed fields other than the RequestID. A reused RequestID with a
// different digest is a different request and is rejected (docs/design
// invariant TestPropRequestDigestImmutable).
func (r Request) Digest() string {
	var b strings.Builder
	put := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
		b.WriteByte(';')
	}
	put("ygg-action-request-v1")
	put(string(r.RepoID))
	put(string(r.Kind))
	put(r.SourceID)
	put(r.Agent)
	put(r.Model)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func errUnknown(what, v string) error { return fmt.Errorf("unknown %s %q", what, v) }
