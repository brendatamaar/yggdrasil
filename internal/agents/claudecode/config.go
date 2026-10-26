package claudecode

// ConfigPaths implements agents.RepoConfigProvider: the files and folders of a
// repo that configure Claude Code itself (settings that can hold hooks, MCP
// servers, sub-agent definitions). The trust scan hashes them (SF-09). The
// list covers ownAgentConfig. Repo config is inert for this adapter (see
// buildArgs); guard.Admit applies the trust policy.
func (*Adapter) ConfigPaths() []string {
	return []string{
		".claude/settings.json",
		".claude/settings.local.json",
		".mcp.json",
		".claude/agents",
		".claude/skills",
		".claude/commands",
		".claude/hooks",
		".claude/rules",
		".claude/output-styles",
		"CLAUDE.md",
		"CLAUDE.local.md",
	}
}

// ConfigNames implements agents.ConfigNameProvider: Claude Code also loads
// these files from nested folders.
func (*Adapter) ConfigNames() []string {
	return []string{"CLAUDE.md", "CLAUDE.local.md", "AGENTS.md"}
}
