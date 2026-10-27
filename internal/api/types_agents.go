package api

// DTOs of the agent list the composer needs (AG-05, AG-13). The UI stays
// agent-neutral: it shows Label and Reason and checks Capabilities, never an
// adapter's name.

// AgentCapabilitiesDTO is the part of agents.Capabilities the composer shows.
type AgentCapabilitiesDTO struct {
	Resume        bool `json:"resume"`
	CostReporting bool `json:"costReporting"`
	// Subagents is "none", "flat" or "nested".
	Subagents string `json:"subagents"`
	// Enforcement is the adapter's enforcement level.
	Enforcement string `json:"enforcement"`
}

// AgentDTO is one registered agent. Reason says in plain language why an
// unavailable agent can't run and what to do about it.
type AgentDTO struct {
	ID           string               `json:"id"`
	Label        string               `json:"label"`
	Available    bool                 `json:"available"`
	Reason       string               `json:"reason,omitempty"`
	Capabilities AgentCapabilitiesDTO `json:"capabilities"`
}

// AgentsResponse is the body of GET /api/agents.
type AgentsResponse struct {
	Agents []AgentDTO `json:"agents"`
}
