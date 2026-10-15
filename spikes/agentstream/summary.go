package agentstream

// SubagentInfo is one sub-agent spawn seen in the stream.
type SubagentInfo struct {
	// ID is the spawning tool_use id; the sub-agent's own events carry it as
	// their ParentID.
	ID          string `json:"id"`
	ParentID    string `json:"parentId,omitempty"`
	Depth       int    `json:"depth"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Isolation   string `json:"isolation,omitempty"`
	// AgentID and SpawnDepth come from the CLI's task_started message.
	AgentID    string `json:"agentId,omitempty"`
	SpawnDepth int    `json:"spawnDepth,omitempty"`
	// Events counts the events whose ParentID is this sub-agent.
	Events int `json:"events"`
}

// Summary is the end-of-run report of `agentstream run`.
type Summary struct {
	Counts    map[Kind]int    `json:"counts"`
	Subagents []*SubagentInfo `json:"subagents"`
	CostUSD   *float64        `json:"costUsd,omitempty"`
	HookCalls int             `json:"hookCalls"`
	Denials   []HookRecord    `json:"denials"`
	Exit      *Exit           `json:"exit,omitempty"`
	RunDir    string          `json:"runDir,omitempty"`

	byID map[string]*SubagentInfo
}

// NewSummary returns an empty summary.
func NewSummary() *Summary {
	return &Summary{Counts: map[Kind]int{}, Subagents: []*SubagentInfo{}, Denials: []HookRecord{}, byID: map[string]*SubagentInfo{}}
}

// Add counts one event.
func (s *Summary) Add(ev Event) {
	s.Counts[ev.Kind]++
	if p := s.byID[ev.ParentID]; p != nil {
		p.Events++
	}
	switch ev.Kind {
	case KindSubagentStart:
		info := &SubagentInfo{ID: ev.ID, ParentID: ev.ParentID, Depth: 1}
		if p := s.byID[ev.ParentID]; p != nil {
			info.Depth = p.Depth + 1
		}
		if ev.Tool != nil {
			in := ParseSubagentInput(ev.Tool.Input)
			info.Type, info.Description, info.Isolation = in.SubagentType, in.Description, in.Isolation
		}
		s.Subagents = append(s.Subagents, info)
		if ev.ID != "" {
			s.byID[ev.ID] = info
		}
	case KindTaskStarted:
		if info := s.byID[ev.ID]; info != nil && ev.ID != "" {
			info.AgentID, info.SpawnDepth = ev.AgentID, ev.Depth
		}
	case KindResult:
		if ev.Usage != nil && ev.Usage.CostUSD != nil {
			s.CostUSD = ev.Usage.CostUSD
		}
	}
}

// AddHooks adds the hook log.
func (s *Summary) AddHooks(recs []HookRecord) {
	s.HookCalls += len(recs)
	for _, r := range recs {
		if r.Decision == DecisionDeny {
			r.Input = nil
			s.Denials = append(s.Denials, r)
		}
	}
}
