package proposals

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Request-bound creation (AC-03, AC-04): a workspace action produces a fixed
// list of pending proposals for one RequestID. The decision "create them,
// reuse the ones a first call made, or refuse a changed list" is pure; the
// shell (internal/proposals) reads what exists, calls PlanRequest and runs the
// plan in one transaction.

// MaxRequestProposals caps the proposals one request may create.
const MaxRequestProposals = 20

// RequestItem is one proposal of a request: who it is for, a plain summary
// (already redacted and clipped by the shell) and its validated action.
type RequestItem struct {
	Task    core.TaskID
	Repo    core.RepoID
	Summary string
	Action  Action
}

// ActionFromDraftRow is ActionFromRow for a request's item: the action service
// sends an already validated draft, whose expected head and diff checksum may
// still be empty (the proposals service pins them before the action is hashed).
// A pin that is present is checked as in ActionFromRow. The result is not
// executable until the service has filled the pins and called Validate.
func ActionFromDraftRow(kind core.ProposalKind, payload []byte) (Action, error) {
	if err := supported(kind); err != nil {
		return nil, err
	}
	a, err := parseKind(kind, payload, false)
	if err != nil {
		return nil, fmt.Errorf("read %s proposal: %w", kind, err)
	}
	return a, nil
}

// RequestProposalID is the ID of the i-th proposal of a request. It is derived
// (a hash, never the request ID itself), so a retried call addresses the same
// rows and a crash cannot leave a request with two proposals for one item.
func RequestProposalID(req core.RequestID, i int) core.ProposalID {
	sum := sha256.Sum256([]byte("ygg-request-proposal\x00" + string(req) + "\x00" + strconv.Itoa(i)))
	return core.ProposalID("prp_" + hex.EncodeToString(sum[:8]))
}

// RequestRefusal says why a request was refused (closed set).
type RequestRefusal string

const (
	// RequestBad: no request ID, no items, or an item without a task or action.
	RequestBad RequestRefusal = "bad_request"
	// RequestTooMany: more than MaxRequestProposals items.
	RequestTooMany RequestRefusal = "too_many"
	// RequestChanged: the request already has proposals and these items differ.
	RequestChanged RequestRefusal = "changed"
)

// RequestPlan is the decision of PlanRequest (sealed).
//
//sumtype:decl
type RequestPlan interface{ isRequestPlan() }

// CreateAll: nothing exists yet; create every item under IDs, in one
// transaction.
type CreateAll struct{ IDs []core.ProposalID }

// ReuseAll: the same list was created before; IDs are its proposals.
type ReuseAll struct{ IDs []core.ProposalID }

// RefuseRequest: create nothing.
type RefuseRequest struct{ Reason RequestRefusal }

func (CreateAll) isRequestPlan()     {}
func (ReuseAll) isRequestPlan()      {}
func (RefuseRequest) isRequestPlan() {}

// PlanRequest decides what a CreateForRequest call does. existing are the
// stored proposals found at RequestProposalID(req, 0..) in order, up to the
// first missing one. A retry that matches them returns the same IDs and
// creates nothing; a different list under the same request is refused, never
// merged. Items match when task, repo, summary and kind agree and the action
// is the stored one apart from the pins (expected head, diff checksum) the
// first call filled in.
func PlanRequest(req core.RequestID, items, existing []RequestItem) RequestPlan {
	switch {
	case req.Validate() != nil || len(items) == 0:
		return RefuseRequest{RequestBad}
	case len(items) > MaxRequestProposals:
		return RefuseRequest{RequestTooMany}
	}
	for _, it := range items {
		if it.Action == nil || it.Task.Validate() != nil || it.Repo.Validate() != nil {
			return RefuseRequest{RequestBad}
		}
	}
	ids := make([]core.ProposalID, len(items))
	for i := range items {
		ids[i] = RequestProposalID(req, i)
	}
	if len(existing) == 0 {
		return CreateAll{IDs: ids}
	}
	if len(existing) != len(items) {
		return RefuseRequest{RequestChanged}
	}
	for i, it := range items {
		if !sameItem(it, existing[i]) {
			return RefuseRequest{RequestChanged}
		}
	}
	return ReuseAll{IDs: ids}
}

func sameItem(in, stored RequestItem) bool {
	if in.Task != stored.Task || in.Repo != stored.Repo || in.Summary != stored.Summary ||
		stored.Action == nil || in.Action.Kind() != stored.Action.Kind() {
		return false
	}
	return Hash(AdoptPins(in.Action, stored.Action)) == Hash(stored.Action)
}

// AdoptPins returns in with each pin it left empty (the expected head of a
// Commit, Push or MergeRequest, the diff checksum of a Commit) taken from
// stored, the action the first call stored. A pin that in sets is kept, so a
// caller that changed one no longer matches.
func AdoptPins(in, stored Action) Action {
	switch v := in.(type) {
	case Commit:
		if s, ok := stored.(Commit); ok {
			if v.ExpectHead == "" {
				v.ExpectHead = s.ExpectHead
			}
			if v.ExpectDiffHash == "" {
				v.ExpectDiffHash = s.ExpectDiffHash
			}
		}
		return v
	case Push:
		if s, ok := stored.(Push); ok && v.ExpectHead == "" {
			v.ExpectHead = s.ExpectHead
		}
		return v
	case MergeRequest:
		if s, ok := stored.(MergeRequest); ok && v.ExpectHead == "" {
			v.ExpectHead = s.ExpectHead
		}
		return v
	}
	return in
}
