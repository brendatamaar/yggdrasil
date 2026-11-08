package worktrees

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// MaxAttempts is how many names are tried for one repo before a collision is
// an error.
const MaxAttempts = 5

// Name is a worktree name; the branch is ygg/<name>. The shell draws names
// and checks their shape; the core only compares them.
type Name string

// Branch is the branch the name gets.
func (n Name) Branch() string { return "ygg/" + string(n) }

// Known is a worktree row already recorded for the task, with the shell's
// answer to "does its folder lie under the worktree root".
type Known struct {
	Worktree core.Worktree
	InRoot   bool
}

// Run is what every state of one Ensure call shares.
type Run struct {
	Task      core.TaskID
	Seq       int // number of the last command issued; inputs must echo it
	Requested []core.RepoID
	Next      int             // index in Requested of the repo being handled
	Known     []Known         // active rows, one per repo
	Placed    []core.Worktree // one per handled repo, in request order
}

// State is a closed set: Idle, Loading, Preparing, Naming, Probing, Adding,
// Recording, Done, Failed.
type State interface{ state() }

// Idle is the start state.
type Idle struct{}

// Loading waits for the task's recorded worktrees.
type Loading struct{ Run Run }

// Preparing waits for the repo's folder check and base commit.
type Preparing struct {
	Run  Run
	Repo core.RepoID
}

// Naming waits for the candidate number Attempt.
type Naming struct {
	Run     Run
	Repo    core.RepoID
	Base    string
	Attempt int
	Tried   []Name // every candidate received so far
}

// Probing waits to hear whether the candidate's branch or folder is taken.
type Probing struct {
	Run       Run
	Repo      core.RepoID
	Base      string
	Attempt   int
	Tried     []Name
	Candidate Name
}

// Adding waits for git to create the worktree.
type Adding struct {
	Run       Run
	Repo      core.RepoID
	Base      string
	Candidate Name
}

// Recording waits for the store to keep the row and its audit entry.
type Recording struct {
	Run      Run
	Repo     core.RepoID
	Base     string
	Worktree core.Worktree
}

// Done means every requested repo has a worktree (new or reused).
type Done struct{ Placed []core.Worktree }

// Failed means a repo could not get one. Placed keeps the worktrees of the
// repos before it; nothing is removed.
type Failed struct {
	Placed  []core.Worktree
	Failure Failure
}

func (Idle) state()      {}
func (Loading) state()   {}
func (Preparing) state() {}
func (Naming) state()    {}
func (Probing) state()   {}
func (Adding) state()    {}
func (Recording) state() {}
func (Done) state()      {}
func (Failed) state()    {}

// FailureKind says why an Ensure call stopped.
type FailureKind string

// The failure kinds.
const (
	FailLoad         FailureKind = "load"          // the task's rows could not be read
	FailOutsideRoot  FailureKind = "outside-root"  // a saved row is not under the worktree folder
	FailUnusablePath FailureKind = "unusable-path" // the repo's folder is not usable (for example WSL)
	FailGit          FailureKind = "git"
	FailCanceled     FailureKind = "canceled"
	FailNameTaken    FailureKind = "name-taken"
	FailRecord       FailureKind = "record"
)

// Failure is the reason and the repo it happened for.
type Failure struct {
	Kind FailureKind
	Repo core.RepoID
}

// Cause is what the shell reports when an effect failed.
type Cause string

// The causes.
const (
	CauseGit      Cause = "git"
	CauseUnusable Cause = "unusable-path"
	CauseCanceled Cause = "canceled"
)

// Input is a closed set of events: Begin and the answers to commands.
type Input interface{ input() }

// Begin starts a call. Only Idle handles it.
type Begin struct {
	Task  core.TaskID
	Repos []core.RepoID
}

// ExistingLoaded answers LoadExisting with the task's recorded rows.
type ExistingLoaded struct {
	Seq      int
	Existing []Known
}

// LoadFailed answers LoadExisting when the rows could not be read.
type LoadFailed struct{ Seq int }

// Prepared answers PrepareRepo: the folder is usable and Base is the commit
// to start from.
type Prepared struct {
	Seq  int
	Base string
}

// PrepareFailed answers PrepareRepo.
type PrepareFailed struct {
	Seq   int
	Cause Cause
}

// NameDrawn answers DrawName with the shell's candidate.
type NameDrawn struct {
	Seq  int
	Name Name
}

// Probed answers CheckName.
type Probed struct {
	Seq   int
	Taken bool
}

// ProbeFailed answers CheckName when the check itself failed.
type ProbeFailed struct {
	Seq   int
	Cause Cause
}

// Added answers AddWorktree. The shell supplies the ids, the time and the
// long-form path, so the core stays free of clocks and id sources.
type Added struct {
	Seq     int
	ID      core.WorktreeID
	AuditID core.AuditID
	Path    string
	At      time.Time
}

// AddFailed answers AddWorktree.
type AddFailed struct {
	Seq   int
	Cause Cause
}

// Recorded answers RecordWorktree.
type Recorded struct{ Seq int }

// RecordFailed answers RecordWorktree.
type RecordFailed struct{ Seq int }

func (Begin) input()          {}
func (ExistingLoaded) input() {}
func (LoadFailed) input()     {}
func (Prepared) input()       {}
func (PrepareFailed) input()  {}
func (NameDrawn) input()      {}
func (Probed) input()         {}
func (ProbeFailed) input()    {}
func (Added) input()          {}
func (AddFailed) input()      {}
func (Recorded) input()       {}
func (RecordFailed) input()   {}

// Command is plain data the shell carries out. Step issues at most one
// command at a time; its answer echoes Seq. There is deliberately no command
// that deletes anything.
type Command interface{ command() }

// LoadExisting asks the store for the task's worktrees (ExistingLoaded or
// LoadFailed).
type LoadExisting struct {
	Seq  int
	Task core.TaskID
}

// PrepareRepo asks the shell to check the repo's folder, resolve the base
// commit and make sure the worktree root exists (Prepared or PrepareFailed).
type PrepareRepo struct {
	Seq  int
	Repo core.RepoID
}

// DrawName asks for candidate number Attempt, 0-based (NameDrawn).
type DrawName struct {
	Seq     int
	Repo    core.RepoID
	Attempt int
}

// CheckName asks whether the branch or the folder for the candidate exists
// (Probed or ProbeFailed).
type CheckName struct {
	Seq    int
	Repo   core.RepoID
	Name   Name
	Branch string
}

// AddWorktree asks git for a worktree on a new branch started at Base (Added
// or AddFailed).
type AddWorktree struct {
	Seq    int
	Repo   core.RepoID
	Name   Name
	Branch string
	Base   string
}

// AuditSpec is the audit entry as data; the shell encodes Detail.
type AuditSpec struct {
	ID      core.AuditID
	At      time.Time
	Actor   string
	Action  string
	Subject string
	Detail  AuditDetail
}

// AuditDetail is what the audit entry of a created worktree records.
type AuditDetail struct {
	Worktree core.WorktreeID
	Repo     core.RepoID
	Branch   string
	Base     string
}

// RecordWorktree asks the store to keep the row, its base commit and the
// audit entry in one transaction (Recorded or RecordFailed).
type RecordWorktree struct {
	Seq      int
	Worktree core.Worktree
	Base     string
	Audit    AuditSpec
}

func (LoadExisting) command()   {}
func (PrepareRepo) command()    {}
func (DrawName) command()       {}
func (CheckName) command()      {}
func (AddWorktree) command()    {}
func (RecordWorktree) command() {}
