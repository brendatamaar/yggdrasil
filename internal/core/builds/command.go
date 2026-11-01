package builds

import "github.com/brendatamaar/yggdrasil/internal/core"

// Command is an effect Step asks the shell for. It is plain data; the core
// never performs it. It is a closed set.
type Command interface{ isCommand() }

// WriteAudit is committed with the state in the same transaction, never after.
type WriteAudit struct {
	Actor   Actor
	Action  string // build.queue, build.start, build.step, build.finish, build.cancel, build.interrupted, build.artifacts
	Subject string // "build:<id>"
	Detail  map[string]string
}

// Publish tells subscribers that the build changed (after commit). They
// refetch by build ID.
type Publish struct{ ID core.BuildID }

// Wake tells the queue worker to run Next again (after commit, coalesced).
type Wake struct{}

// StartStep starts a process. It is not idempotent: the shell refuses a
// second start for the key (BuildID, Index, Attempt).
type StartStep struct {
	BuildID core.BuildID
	Index   int
	Attempt int
	RepoID  core.RepoID
	Target  Target
	Argv    []string
	// Env is the step's extra environment (KEY=VALUE), on top of the clean one.
	Env []string
}

// KillStep cancels the step's context. It is a no-op if the step already
// exited or is unknown.
type KillStep struct {
	BuildID core.BuildID
	Index   int
	Attempt int
}

// CollectArtifacts globs the toolchains' artifact patterns (read only).
type CollectArtifacts struct {
	BuildID core.BuildID
	Target  Target
	RepoIDs []core.RepoID
}

func (WriteAudit) isCommand()       {}
func (Publish) isCommand()          {}
func (Wake) isCommand()             {}
func (StartStep) isCommand()        {}
func (KillStep) isCommand()         {}
func (CollectArtifacts) isCommand() {}
