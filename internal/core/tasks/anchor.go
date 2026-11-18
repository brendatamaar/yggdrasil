package tasks

import (
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Proposal anchors (AC-01, AC-05, AU-02). A proposal hangs under a task, and a
// workspace action such as a pull has none, so each repo gets ONE fixed Ask
// draft (core.SourceProposalAnchor) that only holds those proposals. It is
// created under a reserved request ID that a client can never send, with a
// fixed title and brief, so a repo rename or any user-chosen ID cannot change
// its payload or make a later request for the repo refuse. It can never start
// an agent.

// ReservedRequestPrefix starts every request ID that Yggdrasil makes itself.
// Client-chosen request IDs must not start with it.
const ReservedRequestPrefix = "anchor-"

// Fixed anchor payload: nothing here depends on the repo's name.
const (
	AnchorTitle = "Workspace action proposals"
	AnchorBrief = "This draft holds the proposals that workspace actions made for this repo (a pull, ignore entries). Nothing runs for it; decide each proposal in Review."
)

// AnchorRequest is the reserved request ID of the anchor of one repo.
func AnchorRequest(repo core.RepoID) core.RequestID {
	return core.RequestID(ReservedRequestPrefix + string(repo))
}

// HasReservedPrefix reports whether id is in the namespace of Yggdrasil's own
// request IDs.
func HasReservedPrefix(id core.RequestID) bool {
	return strings.HasPrefix(strings.ToLower(string(id)), ReservedRequestPrefix)
}

// ParseClientRequestID parses a request ID that came from a client: well
// formed, bounded and outside the reserved namespace.
func ParseClientRequestID(v string, maxLen int) (core.RequestID, error) {
	id := core.RequestID(v)
	if id.Validate() != nil || len(v) > maxLen || HasReservedPrefix(id) {
		return "", &UserError{Message: "The request ID isn't valid. Reload the page and try again."}
	}
	return id, nil
}

// StartRefusal decides whether a stored draft may start an agent. It returns a
// plain-language refusal for the internal drafts and nil for every other task.
func StartRefusal(src *core.TaskSource) *UserError {
	if src == nil {
		return nil
	}
	switch src.Kind {
	case core.SourceRepoSetup:
		return &UserError{Message: "Repo setup drafts cannot start an agent."}
	case core.SourceProposalAnchor:
		return &UserError{Message: "This draft only holds proposals for this repo and cannot be started."}
	case core.SourceUser, core.SourceIssue:
		return nil
	}
	return &UserError{Message: "This draft cannot be started."}
}
