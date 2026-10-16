import { describe, expect, it } from "vitest";
import {
  EventMessage,
  EventSubagentEnd,
  EventSubagentStart,
  EventToolCall,
  EventUsage,
  type Event as AgentEvent,
} from "../../api/gen/agents.gen";
import { agentStats, buildTree, taskTotals } from "./tree";

let clock = Date.parse("2026-10-16T10:00:00.000Z");
function ev(seq: number, kind: string, extra: Partial<AgentEvent> = {}): AgentEvent {
  clock += 1000;
  return { seq, kind, at: new Date(clock).toISOString(), ...extra };
}

describe("buildTree", () => {
  it("nests events under their sub-agent, two levels deep", () => {
    const events = [
      ev(1, EventMessage, { id: "m1", text: "start" }),
      ev(2, EventSubagentStart, { id: "sa1", text: "review" }),
      ev(3, EventToolCall, { id: "t1", parentId: "sa1", tool: { name: "Read" } }),
      ev(4, EventSubagentStart, { id: "sa2", parentId: "sa1" }),
      ev(5, EventToolCall, { id: "t2", parentId: "sa2", tool: { name: "Grep" } }),
      ev(6, EventSubagentEnd, { parentId: "sa2" }),
      ev(7, EventSubagentEnd, { parentId: "sa1" }),
      ev(8, EventMessage, { id: "m2", text: "done" }),
    ];
    const roots = buildTree(events);
    expect(roots.map((r) => r.event.seq)).toEqual([1, 2, 8]);
    const sa1 = roots[1];
    expect(sa1?.children.map((c) => c.event.seq)).toEqual([3, 4, 7]);
    const sa2 = sa1?.children[1];
    expect(sa2?.children.map((c) => c.event.seq)).toEqual([5, 6]);
    expect(roots.every((r) => !r.orphan)).toBe(true);
  });

  it("orders by seq whatever the input order", () => {
    const a = ev(1, EventSubagentStart, { id: "sa" });
    const b = ev(2, EventToolCall, { parentId: "sa", tool: { name: "Read" } });
    const c = ev(3, EventMessage, { text: "x" });
    expect(buildTree([c, b, a]).map((r) => r.event.seq)).toEqual([1, 3]);
    expect(buildTree([c, b, a])[0]?.children[0]?.event.seq).toBe(2);
  });

  it("attaches an event whose parent is unknown to the root and flags it", () => {
    const roots = buildTree([ev(1, EventMessage, { text: "a" }), ev(2, EventToolCall, { parentId: "ghost", tool: { name: "Read" } })]);
    expect(roots).toHaveLength(2);
    expect(roots[1]?.orphan).toBe(true);
    expect(roots[0]?.orphan).toBe(false);
  });

  it("does not treat a repeated message ID as a parent", () => {
    // The stream repeats a message ID for each block it holds.
    const roots = buildTree([
      ev(1, EventMessage, { id: "msg_1", text: "a" }),
      ev(2, EventUsage, { id: "msg_1", usage: { inputTokens: 1 } }),
      ev(3, EventToolCall, { parentId: "msg_1", tool: { name: "Read" } }),
    ]);
    expect(roots[0]?.children).toHaveLength(0);
    expect(roots[2]?.orphan).toBe(true);
  });

  it("handles an empty list", () => {
    expect(buildTree([])).toEqual([]);
  });
});

describe("agentStats", () => {
  it("counts tool calls, tokens and the time between start and end", () => {
    const events = [
      ev(1, EventSubagentStart, { id: "sa" }), // t+1s
      ev(2, EventToolCall, { parentId: "sa", tool: { name: "Read" } }), // t+2s
      ev(3, EventUsage, { parentId: "sa", usage: { inputTokens: 100, outputTokens: 20, costUsd: 0.5 } }),
      ev(4, EventToolCall, { parentId: "sa", tool: { name: "Grep" } }),
      ev(5, EventSubagentEnd, { parentId: "sa" }), // t+5s
      ev(6, EventMessage, { text: "after" }), // t+6s, outside
    ];
    const sa = buildTree(events)[0];
    expect(sa).toBeDefined();
    if (!sa) return;
    const s = agentStats(sa);
    expect(s.toolCalls).toBe(2);
    expect(s.inputTokens).toBe(100);
    expect(s.outputTokens).toBe(20);
    expect(s.costUsd).toBe(0.5);
    expect(s.durationMs).toBe(4000);
  });

  it("includes what nested sub-agents did", () => {
    const sa = buildTree([
      ev(1, EventSubagentStart, { id: "a" }),
      ev(2, EventSubagentStart, { id: "b", parentId: "a" }),
      ev(3, EventToolCall, { parentId: "b", tool: { name: "Read" } }),
    ])[0];
    expect(sa && agentStats(sa).toolCalls).toBe(1);
  });

  it("has no duration when the times can't be read", () => {
    const sa = buildTree([{ seq: 1, kind: EventSubagentStart, at: "not a time", id: "a" }])[0];
    expect(sa && agentStats(sa).durationMs).toBeUndefined();
  });
});

describe("taskTotals", () => {
  it("sums usage over the whole task", () => {
    const t = taskTotals([
      ev(1, EventUsage, { usage: { inputTokens: 10, outputTokens: 2 } }),
      ev(2, EventUsage, { parentId: "sa", usage: { inputTokens: 5, outputTokens: 1 } }),
      ev(3, EventUsage, { usage: { costUsd: 0.25, turns: 3 } }),
      ev(4, EventMessage, { text: "ignored" }),
    ]);
    expect(t).toEqual({ inputTokens: 15, outputTokens: 3, costUsd: 0.25, turns: 3 });
  });
});
