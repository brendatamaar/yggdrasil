package proposals

import (
	"github.com/brendatamaar/yggdrasil/internal/core"
)

// RelPath is a path relative to a worktree or checkout root, with forward
// slashes. The parser rejects absolute paths, "..", drives, streams (":"),
// device names and empty paths.
type RelPath string

// Target is where an action acts (sealed): a task worktree or a repo's main
// checkout.
type Target interface{ isTarget() }

// InWorktree acts in one task worktree.
type InWorktree struct{ ID core.WorktreeID }

// MainCheckout acts in the main checkout of a repo.
type MainCheckout struct{ Repo core.RepoID }

func (InWorktree) isTarget()   {}
func (MainCheckout) isTarget() {}

// Action is what a proposal would do once confirmed (sealed): Commit, Push,
// MergeRequest, AgentFile, DeleteFiles or Install. It is immutable after
// creation; a proposal that needs a change is rejected and a new one created.
type Action interface {
	// Kind is the stored kind.
	Kind() core.ProposalKind
	isAction()
}

// Commit commits exactly Paths (never "all").
type Commit struct {
	Where          Target
	Paths          []RelPath
	Message        string
	ExpectHead     string
	ExpectDiffHash string
}

// Push pushes Branch to Remote; no force, ever.
type Push struct {
	Where      Target
	Branch     string
	Remote     string
	ExpectHead string
}

// MergeRequest pushes the branch and opens a merge request with GitLab push
// options. The description is one line (push options cannot carry more).
type MergeRequest struct {
	Where        Target
	Branch       string
	Remote       string
	TargetBranch string
	Title        string
	Description  string
	ExpectHead   string
}

// AgentFile writes one of the protected agent-config files.
type AgentFile struct {
	Where   Target
	Path    RelPath
	Content string
	// ExpectHash is the hash of the file now, or "" when it does not exist.
	ExpectHash string
}

// DeleteFiles moves Paths into _to_delete/; it never deletes.
type DeleteFiles struct {
	Where Target
	Paths []RelPath
}

// Install runs the toolchain's install step by ID, in the main checkout.
type Install struct {
	Repo core.RepoID
	Step string
}

func (Commit) Kind() core.ProposalKind       { return core.ProposalCommit }
func (Push) Kind() core.ProposalKind         { return core.ProposalPush }
func (MergeRequest) Kind() core.ProposalKind { return core.ProposalMergeRequest }
func (AgentFile) Kind() core.ProposalKind    { return core.ProposalAgentFile }
func (DeleteFiles) Kind() core.ProposalKind  { return core.ProposalDeleteFiles }
func (Install) Kind() core.ProposalKind      { return core.ProposalInstall }

func (Commit) isAction()       {}
func (Push) isAction()         {}
func (MergeRequest) isAction() {}
func (AgentFile) isAction()    {}
func (DeleteFiles) isAction()  {}
func (Install) isAction()      {}

// Line is one line of the confirmation dialog.
type Line struct{ Text string }
