import { describe, expect, it } from "vitest";
import {
  EventMessage,
  EventSubagentEnd,
  EventSubagentStart,
  EventToolCall,
  EventUsage,
  type Event as AgentEvent,
  type Usage,
} from "../../api/gen/agents.gen";
import { agentStats, buildTree, groupTurns, taskUsage } from "./tree";

let clock = Date.parse("2026-10-16T10:00:00.000Z");
function ev(seq: number, kind: string, extra: Partial<AgentEvent> = {}): AgentEvent {
  clock += 1000;
  return { seq, kind, at: new Date(clock).toISOString(), ...extra };
}
const usage = (u: Usage, seq: number, parentId?: string) => ev(seq, EventUsage, { usage: u, ...(parentId ? { parentId } : {}) });

describe("buildTree", () => {
  it("nests events under their sub-agent, two levels deep", () => {
    const events = [
      ev(1, EventMessage, { id: "m1" }),
      ev(2, EventSubagentStart, { id: "sa1", text: "review" }),
      ev(3, EventToolCall, { id: "t1", parentId: "sa1", tool: { name: "Read" } }),
      ev(4, EventSubagentStart, { id: "sa2", parentId: "sa1" }),
      ev(5, EventToolCall, { id: "t2", parentId: "sa2", tool: { name: "Grep" } }),
      ev(6, EventSubagentEnd, { parentId: "sa2" }),
      ev(7, EventSubagentEnd, { parentId: "sa1" }),
      ev(8, EventMessage, { id: "m2" }),
    ];
    const roots = buildTree(events);
    expect(roots.map((r) => r.event.seq)).toEqual([1, 2, 8]);
    const sa1 = roots[1];
    expect(sa1?.children.map((c) => c.event.seq)).toEqual([3, 4, 7]);
    expect(sa1?.children[1]?.children.map((c) => c.event.seq)).toEqual([5, 6]);
    expect(roots.every((r) => !r.orphan)).toBe(true);
  });

  it("puts an orphan at the root and marks it", () => {
    const roots = buildTree([ev(1, EventToolCall, { id: "t1", parentId: "ghost" }), ev(2, EventMessage)]);
    expect(roots.map((r) => [r.event.seq, r.orphan])).toEqual([
      [1, true],
      [2, false],
    ]);
  });

  it("treats a child that comes before its parent (by seq) as an orphan", () => {
    const roots = buildTree([ev(1, EventToolCall, { parentId: "sa" }), ev(2, EventSubagentStart, { id: "sa" })]);
    expect(roots[0]?.orphan).toBe(true);
    expect(roots[1]?.children).toHaveLength(0);
  });

  it("orders by seq whatever the input order", () => {
    const a = ev(1, EventSubagentStart, { id: "sa" });
    const b = ev(2, EventToolCall, { parentId: "sa" });
    const roots = buildTree([b, a]);
    expect(roots).toHaveLength(1);
    expect(roots[0]?.children[0]?.event.seq).toBe(2);
    expect(roots[0]?.children[0]?.orphan).toBe(false);
  });

  it("lets the first event win when IDs repeat", () => {
    const roots = buildTree([
      ev(1, EventSubagentStart, { id: "x" }),
      ev(2, EventSubagentStart, { id: "x" }),
      ev(3, EventToolCall, { parentId: "x" }),
    ]);
    expect(roots[0]?.children.map((c) => c.event.seq)).toEqual([3]);
    expect(roots[1]?.children).toHaveLength(0);
  });
});

describe("usage", () => {
  it("sums increments including sub-agent usage", () => {
    const t = taskUsage([
      ev(1, EventSubagentStart, { id: "sa" }),
      usage({ inputTokens: 100, outputTokens: 10, costUsd: 0.01, turns: 1 }, 2),
      usage({ inputTokens: 50, outputTokens: 5, costUsd: 0.005, turns: 1 }, 3, "sa"),
    ]);
    expect(t).toMatchObject({ inputTokens: 150, outputTokens: 15, turns: 2 });
    expect(t.costUsd).toBeCloseTo(0.015);
  });

  it("lets a Cumulative event replace the total, then keeps adding", () => {
    const t = taskUsage([
      usage({ inputTokens: 100, costUsd: 1 }, 1),
      usage({ inputTokens: 500, outputTokens: 50, costUsd: 0.2, cumulative: true }, 2),
      usage({ inputTokens: 10 }, 3),
    ]);
    expect(t.inputTokens).toBe(510);
    expect(t.outputTokens).toBe(50);
    expect(t.costUsd).toBeCloseTo(0.2);
  });

  it("leaves cost undefined when nothing reports it", () => {
    expect(taskUsage([usage({ inputTokens: 1 }, 1)]).costUsd).toBeUndefined();
  });

  it("reports per sub-agent stats", () => {
    const [sa] = buildTree([
      ev(1, EventSubagentStart, { id: "sa" }),
      ev(2, EventToolCall, { parentId: "sa" }),
      usage({ inputTokens: 7, outputTokens: 3 }, 3, "sa"),
      ev(4, EventSubagentEnd, { parentId: "sa" }),
    ]);
    if (!sa) throw new Error("no root");
    const s = agentStats(sa);
    expect(s.toolCalls).toBe(1);
    expect(s.usage.inputTokens + s.usage.outputTokens).toBe(10);
    expect(s.durationMs).toBe(3000);
  });
});

describe("groupTurns", () => {
  it("starts a turn at a message that follows tool work", () => {
    const roots = buildTree([
      ev(1, EventMessage),
      ev(2, EventToolCall),
      ev(3, EventMessage),
      ev(4, EventToolCall),
    ]);
    const turns = groupTurns(roots);
    expect(turns.map((t) => t.nodes.length)).toEqual([2, 2]);
    expect(turns[0]?.durationMs).toBe(1000);
  });
});
