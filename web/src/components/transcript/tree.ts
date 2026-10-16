import {
  EventCommand,
  EventFileEdit,
  EventSubagentEnd,
  EventSubagentStart,
  EventToolCall,
  EventUsage,
  type Event as AgentEvent,
} from "../../api/gen/agents.gen";

/** One event with the events that belong to it (AG-21). */
export interface TreeNode {
  event: AgentEvent;
  children: TreeNode[];
  /**
   * The event names a parent we never received, so it hangs from the root.
   * It happens when events arrive out of order or one is missing.
   */
  orphan: boolean;
}

/**
 * Builds the nested transcript from a flat event list. A sub-agent's start
 * event is the parent of everything it did, through `parentId`. Input order
 * doesn't matter: events are ordered by `seq` first, and a repeated `seq` is
 * the caller's problem (see mergeEvents).
 */
export function buildTree(events: readonly AgentEvent[]): TreeNode[] {
  const sorted = [...events].sort((a, b) => a.seq - b.seq);
  const starts = new Map<string, TreeNode>();
  const roots: TreeNode[] = [];
  for (const event of sorted) {
    const node: TreeNode = { event, children: [], orphan: false };
    if (event.parentId) {
      const parent = starts.get(event.parentId);
      if (parent) {
        parent.children.push(node);
      } else {
        node.orphan = true;
        roots.push(node);
      }
    } else {
      roots.push(node);
    }
    // Only sub-agent starts can be parents: message IDs repeat across events.
    if (event.kind === EventSubagentStart && event.id) starts.set(event.id, node);
  }
  return roots;
}

export interface AgentStats {
  /** Tool calls, commands and file edits, nested sub-agents included. */
  toolCalls: number;
  durationMs?: number;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
}

function walk(node: TreeNode, visit: (n: TreeNode) => void): void {
  for (const child of node.children) {
    visit(child);
    walk(child, visit);
  }
}

function ms(iso: string): number | undefined {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? undefined : t;
}

/** What one sub-agent did: its tool calls, how long it ran and what it used. */
export function agentStats(node: TreeNode): AgentStats {
  const stats: AgentStats = { toolCalls: 0, inputTokens: 0, outputTokens: 0, costUsd: 0 };
  let last = ms(node.event.at);
  const start = last;
  walk(node, (n) => {
    const e = n.event;
    if (e.kind === EventToolCall || e.kind === EventCommand || e.kind === EventFileEdit) stats.toolCalls++;
    if (e.kind === EventUsage && e.usage) {
      stats.inputTokens += e.usage.inputTokens ?? 0;
      stats.outputTokens += e.usage.outputTokens ?? 0;
      stats.costUsd += e.usage.costUsd ?? 0;
    }
    const t = ms(e.at);
    if (t !== undefined && (last === undefined || t > last)) last = t;
    // The end event is the exact finish.
    if (e.kind === EventSubagentEnd && t !== undefined && n.event.parentId === node.event.id) last = t;
  });
  if (start !== undefined && last !== undefined) stats.durationMs = Math.max(0, last - start);
  return stats;
}

/** Totals of a whole task: every usage event, sub-agents included. */
export function taskTotals(events: readonly AgentEvent[]): Omit<AgentStats, "toolCalls" | "durationMs"> & { turns: number } {
  const t = { inputTokens: 0, outputTokens: 0, costUsd: 0, turns: 0 };
  for (const e of events) {
    if (e.kind !== EventUsage || !e.usage) continue;
    t.inputTokens += e.usage.inputTokens ?? 0;
    t.outputTokens += e.usage.outputTokens ?? 0;
    t.costUsd += e.usage.costUsd ?? 0;
    t.turns += e.usage.turns ?? 0;
  }
  return t;
}
