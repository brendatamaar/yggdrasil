package mcp

import (
	"encoding/json"
	"strings"

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

// The wire shapes of the arguments, one per tool. They are used both to decode
// (unknown fields refused) and to encode, so ParseCall and FormatCall agree.
type (
	wNone struct{}
	wRepo struct {
		Repo string `json:"repo"`
	}
	wCommits struct {
		Repo  string `json:"repo"`
		Limit *int   `json:"limit,omitempty"`
	}
	wDiff struct {
		Repo     string `json:"repo"`
		Path     string `json:"path,omitempty"`
		MaxBytes *int   `json:"max_bytes,omitempty"`
	}
	wLimit struct {
		Limit *int `json:"limit,omitempty"`
	}
	wBuildLog struct {
		Build    string `json:"build"`
		Step     int    `json:"step,omitempty"`
		MaxBytes *int   `json:"max_bytes,omitempty"`
	}
	wStartBuild struct {
		Repos []string `json:"repos"`
		Steps []string `json:"steps,omitempty"`
	}
	wPreset struct {
		Preset string `json:"preset"`
	}
	wName struct {
		Name string `json:"name"`
	}
	wCommit struct {
		Repo    string   `json:"repo"`
		Paths   []string `json:"paths"`
		Message string   `json:"message"`
		Summary string   `json:"summary,omitempty"`
	}
	wPush struct {
		Repo    string `json:"repo"`
		Branch  string `json:"branch"`
		Remote  string `json:"remote"`
		Summary string `json:"summary,omitempty"`
	}
	wMR struct {
		Repo        string `json:"repo"`
		Branch      string `json:"branch"`
		Remote      string `json:"remote"`
		Target      string `json:"target"`
		Title       string `json:"title"`
		Description string `json:"description,omitempty"`
		Summary     string `json:"summary,omitempty"`
	}
	wInstall struct {
		Repo    string `json:"repo"`
		Summary string `json:"summary,omitempty"`
	}
	wAgentFile struct {
		Repo    string `json:"repo"`
		Path    string `json:"path"`
		Content string `json:"content"`
		Summary string `json:"summary,omitempty"`
	}
	wDelete struct {
		Repo    string   `json:"repo"`
		Paths   []string `json:"paths"`
		Summary string   `json:"summary,omitempty"`
	}
)

func decodeArgs(args []byte, v any) error {
	if len(args) > MaxRequestBytes {
		return bad("the arguments are over %d bytes", MaxRequestBytes)
	}
	trimmed := strings.TrimSpace(string(args))
	if trimmed == "" || trimmed == "null" {
		trimmed = "{}"
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return bad("the arguments are not what this tool accepts (%v)", err)
	}
	if dec.InputOffset() != int64(len(trimmed)) {
		return bad("the arguments have extra data after the JSON object")
	}
	return nil
}

func orDefault(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func refs(in []string) []RepoRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]RepoRef, len(in))
	for i, s := range in {
		out[i] = RepoRef(s)
	}
	return out
}

func strs(in []RepoRef) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

func nilIfEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return in
}

func intp(n int) *int { return &n }

func build(name string, args []byte) (Call, error) {
	switch Tool(name) {
	case ToolListRepos:
		return ListRepos{}, decodeArgs(args, &wNone{})
	case ToolBuildOrder:
		return BuildOrder{}, decodeArgs(args, &wNone{})
	case ToolListBuildPresets:
		return ListBuildPresets{}, decodeArgs(args, &wNone{})
	case ToolListDevServers:
		return ListDevServers{}, decodeArgs(args, &wNone{})
	case ToolRepoStatus, ToolRemoteStatus, ToolChangedFiles, ToolBuildInfo, ToolFetch, ToolPull:
		var w wRepo
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		r := RepoRef(w.Repo)
		switch Tool(name) {
		case ToolRepoStatus:
			return RepoStatus{Repo: r}, nil
		case ToolRemoteStatus:
			return RemoteStatus{Repo: r}, nil
		case ToolChangedFiles:
			return ChangedFiles{Repo: r}, nil
		case ToolBuildInfo:
			return BuildInfo{Repo: r}, nil
		case ToolFetch:
			return Fetch{Repo: r}, nil
		}
		return Pull{Repo: r}, nil
	case ToolRecentCommits:
		var w wCommits
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return RecentCommits{Repo: RepoRef(w.Repo), Limit: orDefault(w.Limit, DefaultListLimit)}, nil
	case ToolGetDiff:
		var w wDiff
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return GetDiff{Repo: RepoRef(w.Repo), Path: w.Path, MaxBytes: orDefault(w.MaxBytes, MaxDiffBytes)}, nil
	case ToolListBuilds:
		var w wLimit
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return ListBuilds{Limit: orDefault(w.Limit, DefaultListLimit)}, nil
	case ToolBuildLog:
		var w wBuildLog
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return BuildLog{Build: core.BuildID(w.Build), Step: w.Step, MaxBytes: orDefault(w.MaxBytes, MaxLogBytes)}, nil
	case ToolStartBuild:
		var w wStartBuild
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return StartBuild{Repos: refs(w.Repos), Steps: nilIfEmpty(w.Steps)}, nil
	case ToolRunBuildPreset:
		var w wPreset
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return RunBuildPreset(w), nil
	case ToolDevServerLog:
		var w wName
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return DevServerLog(w), nil
	case ToolProposeCommit:
		var w wCommit
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return ProposeCommit{Repo: RepoRef(w.Repo), Paths: nilIfEmpty(w.Paths), Message: w.Message, Summary: w.Summary}, nil
	case ToolProposePush:
		var w wPush
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return ProposePush{Repo: RepoRef(w.Repo), Branch: w.Branch, Remote: w.Remote, Summary: w.Summary}, nil
	case ToolProposeMR:
		var w wMR
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return ProposeMergeRequest{Repo: RepoRef(w.Repo), Branch: w.Branch, Remote: w.Remote, Target: w.Target,
			Title: w.Title, Description: w.Description, Summary: w.Summary}, nil
	case ToolProposeInstall:
		var w wInstall
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return ProposeInstall{Repo: RepoRef(w.Repo), Summary: w.Summary}, nil
	case ToolProposeAgentFile:
		var w wAgentFile
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return ProposeAgentFile{Repo: RepoRef(w.Repo), Path: w.Path, Content: w.Content, Summary: w.Summary}, nil
	case ToolProposeDelete:
		var w wDelete
		if err := decodeArgs(args, &w); err != nil {
			return nil, err
		}
		return ProposeDeleteFiles{Repo: RepoRef(w.Repo), Paths: nilIfEmpty(w.Paths), Summary: w.Summary}, nil
	}
	return nil, bad("%q is not a tool of this server", name)
}

// ParseCall turns a tool name and its JSON arguments into a typed Call. It
// rejects an unknown tool, an unknown field, a wrong type, trailing data, a
// repo that is not a name or ID shape, MaxBytes over the cap, the
// relative-path rules of the proposals design for paths, and build step lists
// over MaxBuildSteps. Missing limit and max_bytes take their defaults. The
// error messages are plain language for the agent.
func ParseCall(name string, args []byte) (Call, error) {
	c, err := build(name, args)
	if err != nil {
		return nil, err
	}
	if err := CheckCall(c); err != nil {
		return nil, err
	}
	return c, nil
}

// FormatCall is the inverse of ParseCall: ParseCall(FormatCall(c)) equals c
// for every call that passes CheckCall. A nil call gives ("", nil).
func FormatCall(c Call) (string, []byte) {
	if c == nil {
		return "", nil
	}
	var w any
	switch v := c.(type) {
	case ListRepos, BuildOrder, ListBuildPresets, ListDevServers:
		w = wNone{}
	case RepoStatus:
		w = wRepo{string(v.Repo)}
	case RemoteStatus:
		w = wRepo{string(v.Repo)}
	case ChangedFiles:
		w = wRepo{string(v.Repo)}
	case BuildInfo:
		w = wRepo{string(v.Repo)}
	case Fetch:
		w = wRepo{string(v.Repo)}
	case Pull:
		w = wRepo{string(v.Repo)}
	case RecentCommits:
		w = wCommits{Repo: string(v.Repo), Limit: intp(v.Limit)}
	case GetDiff:
		w = wDiff{Repo: string(v.Repo), Path: v.Path, MaxBytes: intp(v.MaxBytes)}
	case ListBuilds:
		w = wLimit{Limit: intp(v.Limit)}
	case BuildLog:
		w = wBuildLog{Build: string(v.Build), Step: v.Step, MaxBytes: intp(v.MaxBytes)}
	case StartBuild:
		w = wStartBuild{Repos: strs(v.Repos), Steps: v.Steps}
	case RunBuildPreset:
		w = wPreset(v)
	case DevServerLog:
		w = wName(v)
	case ProposeCommit:
		w = wCommit{string(v.Repo), v.Paths, v.Message, v.Summary}
	case ProposePush:
		w = wPush{string(v.Repo), v.Branch, v.Remote, v.Summary}
	case ProposeMergeRequest:
		w = wMR{string(v.Repo), v.Branch, v.Remote, v.Target, v.Title, v.Description, v.Summary}
	case ProposeInstall:
		w = wInstall{string(v.Repo), v.Summary}
	case ProposeAgentFile:
		w = wAgentFile{string(v.Repo), v.Path, v.Content, v.Summary}
	case ProposeDeleteFiles:
		w = wDelete{string(v.Repo), v.Paths, v.Summary}
	default:
		return string(c.Tool()), nil
	}
	b, err := json.Marshal(w)
	if err != nil {
		// Unreachable: every wire type is plain strings, ints and slices.
		return string(c.Tool()), nil
	}
	return string(c.Tool()), b
}
