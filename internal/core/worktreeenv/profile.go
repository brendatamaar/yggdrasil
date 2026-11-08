package worktreeenv

import "github.com/brendatamaar/yggdrasil/internal/core"

// Job is the profile-copy state of one new worktree. It is a closed set:
// JobIdle, Observing, Copying and JobDone.
type Job interface{ isJob() }

// JobIdle has not started.
type JobIdle struct{}

// Observing waits for Lstat results of every entry.
type Observing struct {
	Worktree core.WorktreeID
	Entries  []RelPath
	Seen     map[RelPath]Observation
	Trigger  Trigger
}

// Observation is what the shell saw for one entry.
type Observation struct {
	Source Source
	Dest   Dest
}

// Copying waits for the planned copies.
type Copying struct {
	Worktree core.WorktreeID
	Pending  []RelPath
	Results  map[RelPath]Result
}

// JobDone has a result for every entry.
type JobDone struct{ Results map[RelPath]Result }

func (JobIdle) isJob()   {}
func (Observing) isJob() {}
func (Copying) isJob()   {}
func (JobDone) isJob()   {}

// Result is the closed set Copied, Skipped, CopyFailed.
type Result interface{ isResult() }

// Copied means the file was created in the worktree.
type Copied struct{}

// Skipped means the entry was left alone for Reason.
type Skipped struct{ Reason SkipReason }

// CopyFailed means the copy failed; Reason is plain language without paths.
type CopyFailed struct{ Reason string }

func (Copied) isResult()     {}
func (Skipped) isResult()    {}
func (CopyFailed) isResult() {}

// ProfileInput is a closed set: CopyRequested, Observed, FileCopied, FileCopyFailed.
type ProfileInput interface{ isProfileInput() }

// CopyRequested starts a job. AgentCopy is the per-repo toggle (off by default).
type CopyRequested struct {
	Worktree  core.WorktreeID
	Trigger   Trigger
	Entries   []RelPath
	AgentCopy bool
	Now       Now
}

// Observed carries the Lstat results for one entry.
type Observed struct {
	Entry  RelPath
	Source Source
	Dest   Dest
}

// FileCopied reports a finished copy.
type FileCopied struct{ Entry RelPath }

// FileCopyFailed reports a failed copy.
type FileCopyFailed struct {
	Entry  RelPath
	Reason string
}

func (CopyRequested) isProfileInput()  {}
func (Observed) isProfileInput()       {}
func (FileCopied) isProfileInput()     {}
func (FileCopyFailed) isProfileInput() {}

// ProfileCommand is a closed set: Observe, CopyFile, ProfileAudit, ProfilePublish.
type ProfileCommand interface{ isProfileCommand() }

// Observe asks the shell to Lstat the entry (no link followed) in the repo
// and the worktree.
type Observe struct{ Entry RelPath }

// CopyFile asks for an exclusive-create copy. Emitted only for an entry
// observed as a regular file with an absent destination (invariant P1).
type CopyFile struct{ Entry RelPath }

// ProfileAudit carries counts and relative names only, never contents.
type ProfileAudit struct {
	Action  string // worktreeenv.profile_copied, worktreeenv.profile_skipped
	Copied  int
	Skipped int
	Failed  int
}

// ProfilePublish tells subscribers the job finished.
type ProfilePublish struct{ Worktree core.WorktreeID }

func (Observe) isProfileCommand()        {}
func (CopyFile) isProfileCommand()       {}
func (ProfileAudit) isProfileCommand()   {}
func (ProfilePublish) isProfileCommand() {}

// Profile is the profile-copy decision. Foundation stub; task E1 implements it.
func Profile(j Job, in ProfileInput) (Job, []ProfileCommand) {
	return j, nil
}
