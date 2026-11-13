package hosts

import (
	"fmt"
	"time"
)

// ResolveInstance selects a configured authority, falling back to the bare host
// only when the parsed remote's authority is not configured. URL parsing and
// default/SSH port normalization belong to the shell; unknown authorities are
// retained so target resolution can explain the missing configuration.
func ResolveInstance(authority, host InstanceID, instances map[InstanceID]Instance) InstanceID {
	if _, ok := instances[authority]; ok {
		return authority
	}
	if _, ok := instances[host]; ok {
		return host
	}
	return authority
}

// ResolveTarget returns the existing explanation for an ineligible linked
// target, or empty when it can be polled. Missing configuration takes precedence
// over detached HEAD. The shell retains the cached link and credential flag.
func ResolveTarget(target RepoTarget, instances map[InstanceID]Instance) string {
	if _, ok := instances[target.Instance]; !ok {
		return fmt.Sprintf("The host %s isn't configured. Add it to the .env file to see its merge requests.", target.Instance)
	}
	if target.Branch == "" {
		return "This repo's HEAD is detached, so there is no branch to look up."
	}
	return ""
}

// PollResponse is a parsed client result, without transport errors or secrets.
//
//sumtype:decl
type PollResponse interface{ isPollResponse() }

// PollData holds an already parsed successful response.
type PollData struct {
	Payload Payload
	ETag    string
}

// PollUnchanged represents a conditional read with no changed data.
type PollUnchanged struct{}

// PollFailure holds the client's classified failure and optional retry deadline.
type PollFailure struct {
	Kind    FailKind
	RetryAt *time.Time
}

// PollUnexpected represents an unparseable response or client defect. The
// existing policy treats these as transient failures of an idempotent read.
type PollUnexpected struct{}

func (PollData) isPollResponse()       {}
func (PollUnchanged) isPollResponse()  {}
func (PollFailure) isPollResponse()    {}
func (PollUnexpected) isPollResponse() {}

// PollResult translates parsed response facts to the existing poller protocol.
// It preserves the command's identity and attempt so Step can reject stale
// results. Now is sampled by the shell only after its cancellation guard.
func PollResult(fetch Fetch, response PollResponse, now time.Time) Input {
	switch v := response.(type) {
	case PollData:
		return Fetched{Key: fetch.Key, Attempt: fetch.Attempt, Payload: v.Payload, ETag: v.ETag, Now: now}
	case PollUnchanged:
		return NotModified{Key: fetch.Key, Attempt: fetch.Attempt, Now: now}
	case PollFailure:
		return FetchFailed{Key: fetch.Key, Attempt: fetch.Attempt, Kind: v.Kind, RetryAt: v.RetryAt, Now: now}
	case PollUnexpected:
		return FetchFailed{Key: fetch.Key, Attempt: fetch.Attempt, Kind: FailTransient, Now: now}
	}
	return nil
}
