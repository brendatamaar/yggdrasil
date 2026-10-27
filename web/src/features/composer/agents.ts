// The API has no agent list yet (reported to the backend). Until then the
// choices are fixed; an agent the server cannot start is refused at create
// time and the error text is shown. Replace with a query when it exists.
export interface AgentChoice {
  id: string;
  label: string;
  available: boolean;
  /** Why it cannot be picked. */
  reason?: string;
}

export const AGENTS: readonly AgentChoice[] = [
  { id: "mock", label: "Demo agent", available: true },
  { id: "claude-code", label: "Claude Code", available: true },
];
