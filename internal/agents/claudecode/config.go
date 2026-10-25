package claudecode

// ConfigPaths implements agents.RepoConfigProvider: the files and folders of a
// repo that configure Claude Code itself (settings that can hold hooks, MCP
// servers, sub-agent definitions). The trust scan hashes them (SF-09). The
// list matches ownAgentConfig, which still refuses such repos until the tasks
// service uses the trust decision instead.
func (*Adapter) ConfigPaths() []string {
	return []string{
		".claude/settings.json",
		".claude/settings.local.json",
		".mcp.json",
		".claude/agents",
		".claude/skills",
		".claude/commands",
		".claude/hooks",
		"CLAUDE.md",
		"CLAUDE.local.md",
	}
}
