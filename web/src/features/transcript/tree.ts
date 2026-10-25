import {
  EventCommand,
  EventFileEdit,
  EventSubagentEnd,
  EventToolCall,
  EventUsage,
  type Event as AgentEvent,
  type Usage,
} from "../../api/gen/agents.gen";

/** One event with the events that belong to it (AG-21). */
export interface TreeNode {
  event: AgentEvent;
  children: TreeNode[];
  /**
   * The event names a parent we never received (or one that comes later), so
   * it hangs from the root and is marked.
   */
  orphan: boolean;
}

/**
 * TypeScript port of agents.BuildTree (internal/agents/runner.go). Events are
 * linked to the event whose ID is their parentId and the roots come back in
 * order. A parent is only found when it comes before its children (by seq),
 * which also rules out cycles. When IDs repeat, the first event wins.
 */
export function buildTree(events: readonly AgentEvent[]): TreeNode[] {
  const sorted = [...events].sort((a, b) => a.seq - b.seq);
  const byId = new Map<string, TreeNode>();
  const roots: TreeNode[] = [];
  for (const event of sorted) {
    const node: TreeNode = { event, children: [], orphan: false };
    if (!event.parentId) {
      roots.push(node);
    } else {
      const parent = byId.get(event.parentId);
      if (parent) {
        parent.children.push(node);
      } else {
        node.orphan = true;
        roots.push(node);
      }
    }
    if (event.id && !byId.has(event.id)) byId.set(event.id, node);
  }
  return roots;
}

/** Usage totals. Cost stays undefined until some event reports one. */
export interface UsageTotals {
  inputTokens: number;
  outputTokens: number;
  cacheWriteTokens: number;
  cacheReadTokens: number;
  costUsd?: number;
  durationMs: number;
  turns: number;
}

export function emptyUsage(): UsageTotals {
  return { inputTokens: 0, outputTokens: 0, cacheWriteTokens: 0, cacheReadTokens: 0, durationMs: 0, turns: 0 };
}

/** Mirror of addUsage in runner.go: an increment adds, a Cumulative event replaces the total. */
export function addUsage(total: UsageTotals, u: Usage): UsageTotals {
  const n = (v: number | undefined) => v ?? 0;
  if (u.cumulative) {
    const next: UsageTotals = {
      inputTokens: n(u.inputTokens),
      outputTokens: n(u.outputTokens),
      cacheWriteTokens: n(u.cacheWriteTokens),
      cacheReadTokens: n(u.cacheReadTokens),
      durationMs: n(u.durationMs),
      turns: n(u.turns),
    };
    if (u.costUsd !== undefined) next.costUsd = u.costUsd;
    return next;
  }
  const next: UsageTotals = {
    inputTokens: total.inputTokens + n(u.inputTokens),
    outputTokens: total.outputTokens + n(u.outputTokens),
    cacheWriteTokens: total.cacheWriteTokens + n(u.cacheWriteTokens),
    cacheReadTokens: total.cacheReadTokens + n(u.cacheReadTokens),
    durationMs: total.durationMs + n(u.durationMs),
    turns: total.turns + n(u.turns),
  };
  if (u.costUsd !== undefined) next.costUsd = u.costUsd + (total.costUsd ?? 0);
  else if (total.costUsd !== undefined) next.costUsd = total.costUsd;
  return next;
}

/** Task total: every usage event in seq order, sub-agents included (AG-21). */
export function taskUsage(events: readonly AgentEvent[]): UsageTotals {
  let total = emptyUsage();
  for (const e of [...events].sort((a, b) => a.seq - b.seq)) {
    if (e.kind === EventUsage && e.usage) total = addUsage(total, e.usage);
  }
  return total;
}

export interface AgentStats {
  /** Tool calls, commands and file edits, nested sub-agents included. */
  toolCalls: number;
  durationMs?: number;
  usage: UsageTotals;
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
  let usage = emptyUsage();
  let toolCalls = 0;
  const start = ms(node.event.at);
  let last = start;
  walk(node, (n) => {
    const e = n.event;
    if (e.kind === EventToolCall || e.kind === EventCommand || e.kind === EventFileEdit) toolCalls++;
    if (e.kind === EventUsage && e.usage) usage = addUsage(usage, e.usage);
    const t = ms(e.at);
    if (t !== undefined && (last === undefined || t > last)) last = t;
    // The end event is the exact finish.
    if (e.kind === EventSubagentEnd && t !== undefined && e.parentId === node.event.id) last = t;
  });
  const stats: AgentStats = { toolCalls, usage };
  if (start !== undefined && last !== undefined) stats.durationMs = Math.max(0, last - start);
  return stats;
}

/** A turn: the agent's work between two messages. */
export interface Turn {
  nodes: TreeNode[];
  durationMs?: number;
}

/**
 * Splits root nodes into turns. A new turn starts at a message once the
 * current turn holds something other than messages, so one reply followed by
 * its tool work stays together.
 */
export function groupTurns(roots: readonly TreeNode[]): Turn[] {
  const turns: Turn[] = [];
  let current: TreeNode[] = [];
  let hasWork = false;
  const flush = () => {
    if (current.length > 0) turns.push({ nodes: current, durationMs: turnDuration(current) });
    current = [];
    hasWork = false;
  };
  for (const n of roots) {
    const isMessage = n.event.kind === "message";
    if (isMessage && hasWork) flush();
    current.push(n);
    if (!isMessage) hasWork = true;
  }
  flush();
  return turns;
}

function turnDuration(nodes: readonly TreeNode[]): number | undefined {
  let first: number | undefined;
  let last: number | undefined;
  const see = (n: TreeNode) => {
    const t = ms(n.event.at);
    if (t === undefined) return;
    if (first === undefined || t < first) first = t;
    if (last === undefined || t > last) last = t;
  };
  for (const n of nodes) {
    see(n);
    walk(n, see);
  }
  return first !== undefined && last !== undefined && last > first ? last - first : undefined;
}
