package hosts

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Input is one event for Step. It is a closed set.
type Input interface{ isInput() }

// ConfigChanged replaces the instances (startup, or a reload of the .env).
type ConfigChanged struct {
	Instances []Instance
	Now       time.Time
}

// RepoTarget links a repo's current branch to a host project.
type RepoTarget struct {
	Repo     core.RepoID
	Instance InstanceID
	Project  ProjectPath
	Branch   string
}

// TargetsChanged carries the full set of linked repos (after a status
// refresh or a tracking change).
type TargetsChanged struct {
	Repos []RepoTarget
	Now   time.Time
}

// Tick is the shell ticker.
type Tick struct{ Now time.Time }

// RefreshRequested asks to refetch keys; empty means all.
type RefreshRequested struct {
	Keys []Key
	Now  time.Time
}

// Fetched is a successful fetch.
type Fetched struct {
	Key     Key
	Attempt int
	Payload Payload
	ETag    string
	Now     time.Time
}

// NotModified is a 304 on a conditional request.
type NotModified struct {
	Key     Key
	Attempt int
	Now     time.Time
}

// FetchFailed is a failed fetch.
type FetchFailed struct {
	Key     Key
	Attempt int
	Kind    FailKind
	// RetryAt comes from the response headers, when it had them.
	RetryAt *time.Time
	Now     time.Time
}

func (ConfigChanged) isInput()    {}
func (TargetsChanged) isInput()   {}
func (Tick) isInput()             {}
func (RefreshRequested) isInput() {}
func (Fetched) isInput()          {}
func (NotModified) isInput()      {}
func (FetchFailed) isInput()      {}

// Command is an effect Step asks for. It is a closed set. Every command is a
// Fetch or a Publish: nothing can write to a host (H9).
type Command interface{ isCommand() }

// Fetch is a GET (2 to 3 requests inside the client), deduplicated by
// (Key, Attempt).
type Fetch struct {
	Key      Key
	Instance InstanceID
	Project  ProjectPath
	Branch   string
	Attempt  int
	ETag     string
}

// PublishKey tells subscribers that a key's data or phase changed.
type PublishKey struct{ Key Key }

// PublishHosts tells subscribers that the instances or their state changed.
type PublishHosts struct{}

func (Fetch) isCommand()        {}
func (PublishKey) isCommand()   {}
func (PublishHosts) isCommand() {}

// Run feeds inputs to Step in order and returns the final state and every
// command in order. The property tests drive Step through it.
func Run(p Poller, inputs []Input) (Poller, []Command) {
	var all []Command
	for _, in := range inputs {
		var cmds []Command
		p, cmds = Step(p, in)
		all = append(all, cmds...)
	}
	return p, all
}

// LinkSet holds the links that always work, even without a token or data.
type LinkSet struct {
	Repo      string // the repo's page on the host
	Branch    string // the branch's page, "" when detached
	MRList    string // the repo's list of open MRs
	Pipelines string // the repo's pipelines page
}
