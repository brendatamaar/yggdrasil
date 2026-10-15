package agents

// SubagentSupport is how far an adapter can show agent-native sub-agents
// (AG-02, AG-21).
type SubagentSupport string

const (
	// SubagentsNone: the agent has no sub-agents, or the adapter can't see them.
	SubagentsNone SubagentSupport = "none"
	// SubagentsFlat: sub-agent activity is visible but not linked to its parent.
	SubagentsFlat SubagentSupport = "flat"
	// SubagentsNested: events carry ParentID, so a tree can be built.
	SubagentsNested SubagentSupport = "nested"
)

// Enforcement is how strongly Yggdrasil's limits hold for an adapter (SF-06).
type Enforcement string

const (
	// EnforcementHook: a pre-tool hook can block calls and fails closed.
	EnforcementHook Enforcement = "hook"
	// EnforcementLimited: limits are only requested (flags, env, prompt). Such
	// adapters are restricted to worktree-only Code mode.
	EnforcementLimited Enforcement = "limited"
)

// Capabilities says what an adapter can do. Code checks these flags instead
// of assuming a feature; the UI hides or greys out what is missing (AG-02).
type Capabilities struct {
	Resume          bool            `json:"resume"`
	CostReporting   bool            `json:"costReporting"`
	PlanLimits      bool            `json:"planLimits"`
	PermissionHooks bool            `json:"permissionHooks"`
	Enforcement     Enforcement     `json:"enforcement"`
	Subagents       SubagentSupport `json:"subagents"`
	// SubagentLimits: depth and concurrency caps can be applied (AG-22).
	SubagentLimits bool `json:"subagentLimits"`
	// BudgetCap: the agent has its own spend cap (e.g. --max-budget-usd).
	// Without it Yggdrasil tracks reported usage and stops the run (AG-16).
	BudgetCap bool `json:"budgetCap"`
}
