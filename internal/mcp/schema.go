package mcp

import cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"

// prop is one argument of a tool, for the JSON schema the agent sees. The
// rules themselves live in core/mcp.ParseCall; the schema only describes them.
type prop struct {
	name, typ, desc string
	required        bool
}

type descriptor struct {
	desc  string
	props []prop
}

func str(name, desc string, req bool) prop  { return prop{name, "string", desc, req} }
func num(name, desc string, req bool) prop  { return prop{name, "integer", desc, req} }
func list(name, desc string, req bool) prop { return prop{name, "array", desc, req} }

const (
	repoDesc    = "Repo name or ID, as listed by list_repos."
	summaryDesc = "One or two sentences telling the user why you propose this."
)

// descriptors describes every tool. Registered tools are exactly the keys of
// this map that core/mcp.Offered allows; TestDescriptorsCoverAllTools keeps the
// two lists equal.
var descriptors = map[cmcp.Tool]descriptor{
	cmcp.ToolListRepos:    {desc: "List the repos of this project (name, ID, default branch, whether you can work in it)."},
	cmcp.ToolRepoStatus:   {desc: "Branch, uncommitted changes and ahead/behind of a repo.", props: []prop{str("repo", repoDesc, true)}},
	cmcp.ToolRemoteStatus: {desc: "Merge requests and pipeline status of a repo from the git host (cached).", props: []prop{str("repo", repoDesc, true)}},
	cmcp.ToolRecentCommits: {desc: "Recent commits of a repo you work in.", props: []prop{
		str("repo", repoDesc, true), num("limit", "How many commits (default 20, at most 200).", false)}},
	cmcp.ToolChangedFiles: {desc: "Files changed in a repo you work in.", props: []prop{str("repo", repoDesc, true)}},
	cmcp.ToolGetDiff: {desc: "Diff of a repo you work in (size-capped).", props: []prop{
		str("repo", repoDesc, true), str("path", "Relative path to limit the diff to.", false),
		num("max_bytes", "Largest answer in bytes (default and maximum 65536).", false)}},
	cmcp.ToolBuildInfo:        {desc: "Detected toolchains and commands of a repo.", props: []prop{str("repo", repoDesc, true)}},
	cmcp.ToolBuildOrder:       {desc: "Order in which the project's repos should be built."},
	cmcp.ToolListBuildPresets: {desc: "Saved build presets of the project."},
	cmcp.ToolListBuilds: {desc: "Recent builds.", props: []prop{
		num("limit", "How many builds (default 20, at most 200).", false)}},
	cmcp.ToolBuildLog: {desc: "Log of one step of a build.", props: []prop{
		str("build", "Build ID from start_build or list_builds.", true), num("step", "Step number, from 0.", false),
		num("max_bytes", "Largest answer in bytes (default and maximum 65536).", false)}},
	cmcp.ToolStartBuild: {desc: "Queue a build of repos you work in. Steps are verify, build or full_tests (default verify). Returns at once; read the result with build_log.", props: []prop{
		list("repos", "Repo names or IDs.", true), list("steps", "Step groups: verify, build, full_tests.", false)}},
	cmcp.ToolRunBuildPreset: {desc: "Run a saved build preset whose repos you all work in.", props: []prop{
		str("preset", "Preset name or ID.", true)}},
	cmcp.ToolListDevServers: {desc: "Running dev servers of the project."},
	cmcp.ToolDevServerLog: {desc: "Log of a dev server.", props: []prop{
		str("name", "Dev server name.", true)}},
	cmcp.ToolFetch: {desc: "git fetch for a repo you work in.", props: []prop{str("repo", repoDesc, true)}},
	cmcp.ToolPull: {desc: "Fast-forward-only pull of a repo's main checkout. Refused when the tree has changes or a build or editor holds the checkout.", props: []prop{
		str("repo", repoDesc, true)}},
	cmcp.ToolProposeCommit: {desc: "Propose a commit of exact paths. The user confirms it; nothing is committed by this call.", props: []prop{
		str("repo", repoDesc, true), list("paths", "Relative file paths to commit (not folders).", true),
		str("message", "Commit message.", true), str("summary", summaryDesc, false)}},
	cmcp.ToolProposePush: {desc: "Propose pushing a branch. The user confirms it; nothing is pushed by this call.", props: []prop{
		str("repo", repoDesc, true), str("branch", "Branch to push.", true), str("remote", "Remote name.", true),
		str("summary", summaryDesc, false)}},
	cmcp.ToolProposeMR: {desc: "Propose pushing a branch and opening a GitLab merge request. The user confirms it.", props: []prop{
		str("repo", repoDesc, true), str("branch", "Source branch.", true), str("remote", "Remote name.", true),
		str("target", "Target branch.", true), str("title", "Merge request title.", true),
		str("description", "One-line description.", false), str("summary", summaryDesc, false)}},
	cmcp.ToolProposeInstall: {desc: "Propose installing dependencies in the main checkout. The user confirms it.", props: []prop{
		str("repo", repoDesc, true), str("summary", summaryDesc, false)}},
	cmcp.ToolProposeAgentFile: {desc: "Propose a change to an agent config file (for example CLAUDE.md). The user confirms it.", props: []prop{
		str("repo", repoDesc, true), str("path", "Relative path of the file.", true),
		str("content", "Full new content of the file.", true), str("summary", summaryDesc, false)}},
	cmcp.ToolProposeDelete: {desc: "Propose removing files. They are moved into a _to_delete folder for the user, never deleted.", props: []prop{
		str("repo", repoDesc, true), list("paths", "Relative file paths.", true), str("summary", summaryDesc, false)}},
}

// schemaOf builds the input schema of a tool as a plain map (the SDK accepts
// any JSON-marshalable value whose type is "object").
func schemaOf(d descriptor) map[string]any {
	props := map[string]any{}
	var req []string
	for _, p := range d.props {
		m := map[string]any{"type": p.typ, "description": p.desc}
		if p.typ == "array" {
			m["items"] = map[string]any{"type": "string"}
		}
		props[p.name] = m
		if p.required {
			req = append(req, p.name)
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(req) > 0 {
		s["required"] = req
	}
	return s
}
