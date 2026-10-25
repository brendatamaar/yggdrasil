package mcp

import (
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// RepoRef names a repo by name or ID. It is resolved against the visible set
// by the shell; anything else is "no such repo".
type RepoRef string

// Call is a parsed tool call (sealed): one variant per tool, all fields typed
// and bounded. ParseCall rejects unknown tools and unknown fields.
type Call interface {
	Tool() Tool
	isCall()
}

type (
	ListRepos     struct{}
	RepoStatus    struct{ Repo RepoRef }
	RemoteStatus  struct{ Repo RepoRef }
	RecentCommits struct {
		Repo  RepoRef
		Limit int
	}
	ChangedFiles struct{ Repo RepoRef }
	GetDiff      struct {
		Repo RepoRef
		// Path is relative; empty means the whole diff.
		Path     string
		MaxBytes int
	}
	BuildInfo        struct{ Repo RepoRef }
	BuildOrder       struct{}
	ListBuildPresets struct{}
	ListBuilds       struct{ Limit int }
	BuildLog         struct {
		Build    core.BuildID
		Step     int
		MaxBytes int
	}
	StartBuild struct {
		Repos []RepoRef
		// Steps are step names such as "verify"; at most MaxBuildSteps.
		Steps []string
	}
	RunBuildPreset struct{ Preset string }
	ListDevServers struct{}
	DevServerLog   struct{ Name string }
	Fetch          struct{ Repo RepoRef }
	Pull           struct{ Repo RepoRef }

	ProposeCommit struct {
		Repo    RepoRef
		Paths   []string
		Message string
		Summary string
	}
	ProposePush struct {
		Repo    RepoRef
		Branch  string
		Remote  string
		Summary string
	}
	ProposeMergeRequest struct {
		Repo        RepoRef
		Branch      string
		Remote      string
		Target      string
		Title       string
		Description string
		Summary     string
	}
	ProposeInstall struct {
		Repo    RepoRef
		Summary string
	}
	ProposeAgentFile struct {
		Repo    RepoRef
		Path    string
		Content string
		Summary string
	}
	ProposeDeleteFiles struct {
		Repo    RepoRef
		Paths   []string
		Summary string
	}
)

func (ListRepos) Tool() Tool           { return ToolListRepos }
func (RepoStatus) Tool() Tool          { return ToolRepoStatus }
func (RemoteStatus) Tool() Tool        { return ToolRemoteStatus }
func (RecentCommits) Tool() Tool       { return ToolRecentCommits }
func (ChangedFiles) Tool() Tool        { return ToolChangedFiles }
func (GetDiff) Tool() Tool             { return ToolGetDiff }
func (BuildInfo) Tool() Tool           { return ToolBuildInfo }
func (BuildOrder) Tool() Tool          { return ToolBuildOrder }
func (ListBuildPresets) Tool() Tool    { return ToolListBuildPresets }
func (ListBuilds) Tool() Tool          { return ToolListBuilds }
func (BuildLog) Tool() Tool            { return ToolBuildLog }
func (StartBuild) Tool() Tool          { return ToolStartBuild }
func (RunBuildPreset) Tool() Tool      { return ToolRunBuildPreset }
func (ListDevServers) Tool() Tool      { return ToolListDevServers }
func (DevServerLog) Tool() Tool        { return ToolDevServerLog }
func (Fetch) Tool() Tool               { return ToolFetch }
func (Pull) Tool() Tool                { return ToolPull }
func (ProposeCommit) Tool() Tool       { return ToolProposeCommit }
func (ProposePush) Tool() Tool         { return ToolProposePush }
func (ProposeMergeRequest) Tool() Tool { return ToolProposeMR }
func (ProposeInstall) Tool() Tool      { return ToolProposeInstall }
func (ProposeAgentFile) Tool() Tool    { return ToolProposeAgentFile }
func (ProposeDeleteFiles) Tool() Tool  { return ToolProposeDelete }

func (ListRepos) isCall()           {}
func (RepoStatus) isCall()          {}
func (RemoteStatus) isCall()        {}
func (RecentCommits) isCall()       {}
func (ChangedFiles) isCall()        {}
func (GetDiff) isCall()             {}
func (BuildInfo) isCall()           {}
func (BuildOrder) isCall()          {}
func (ListBuildPresets) isCall()    {}
func (ListBuilds) isCall()          {}
func (BuildLog) isCall()            {}
func (StartBuild) isCall()          {}
func (RunBuildPreset) isCall()      {}
func (ListDevServers) isCall()      {}
func (DevServerLog) isCall()        {}
func (Fetch) isCall()               {}
func (Pull) isCall()                {}
func (ProposeCommit) isCall()       {}
func (ProposePush) isCall()         {}
func (ProposeMergeRequest) isCall() {}
func (ProposeInstall) isCall()      {}
func (ProposeAgentFile) isCall()    {}
func (ProposeDeleteFiles) isCall()  {}

// ParseCall turns a tool name and its JSON arguments into a typed Call. It
// rejects an unknown tool, an unknown field, a wrong type, a repo that is not
// a name or ID shape, MaxBytes over the cap, the relative-path rules of the
// proposals design for paths, and build step lists over MaxBuildSteps.
//
// STUB (task M1 implements it): always an error, so no call is ever parsed.
func ParseCall(name string, args []byte) (Call, error) {
	_ = args
	return nil, fmt.Errorf("parse call %q: %w", name, ErrNotImplemented)
}

// FormatCall is the inverse of ParseCall: ParseCall(FormatCall(c)) equals c.
//
// STUB (task M1 implements it): returns the tool name and nil arguments.
func FormatCall(c Call) (string, []byte) {
	if c == nil {
		return "", nil
	}
	return string(c.Tool()), nil
}
