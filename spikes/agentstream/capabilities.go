package agentstream

// SubagentSupport is how far an adapter can show sub-agents (AG-02).
type SubagentSupport string

const (
	SubagentsNone   SubagentSupport = "none"
	SubagentsFlat   SubagentSupport = "flat"
	SubagentsNested SubagentSupport = "nested"
)

// Enforcement is how strongly an adapter's limits hold.
type Enforcement string

const (
	// EnforcementHook: a pre-tool hook can block calls (fail closed).
	EnforcementHook Enforcement = "hook"
	// EnforcementAdvisory: limits are only asked for (env, settings, prompt).
	EnforcementAdvisory Enforcement = "advisory"
)

// Capabilities is the draft AG-02 shape for Phase 1 internal/agents.
type Capabilities struct {
	Resume          bool            `json:"resume"`
	CostReporting   bool            `json:"costReporting"`
	PlanLimits      bool            `json:"planLimits"`
	PermissionHooks bool            `json:"permissionHooks"`
	Enforcement     Enforcement     `json:"enforcement"`
	Subagents       SubagentSupport `json:"subagents"`
	// SubagentLimits: depth and concurrency caps can be applied (AG-22).
	SubagentLimits bool `json:"subagentLimits"`
	BudgetCap      bool `json:"budgetCap"`
}

// ClaudeCode is what this spike expects of Claude Code. Documented (CLI
// 2.1.299 help and docs): resume (--resume), cost (total_cost_usd), hooks,
// nested sub-agents (parent_tool_use_id at every depth), the limit env vars
// and --max-budget-usd. Not yet seen in a live run. PlanLimits is unknown and
// left false.
var ClaudeCode = Capabilities{
	Resume:          true,
	CostReporting:   true,
	PermissionHooks: true,
	Enforcement:     EnforcementHook,
	Subagents:       SubagentsNested,
	SubagentLimits:  true,
	BudgetCap:       true,
}
