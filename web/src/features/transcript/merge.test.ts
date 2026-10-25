import { describe, expect, it } from "vitest";
import { EventMessage, type Event as AgentEvent } from "../../api/gen/agents.gen";
import { asEvent, contiguousSeq, hasGap, mergeEvents } from "./merge";

const ev = (seq: number, text = ""): AgentEvent => ({ seq, kind: EventMessage, at: "2026-10-16T10:00:00Z", text });

describe("mergeEvents", () => {
  it("orders by seq and keeps one event per seq", () => {
    const merged = mergeEvents([ev(1), ev(3)], [ev(2), ev(3, "newer"), ev(1)]);
    expect(merged.map((e) => e.seq)).toEqual([1, 2, 3]);
    expect(merged[2]?.text).toBe("newer");
  });

  it("is a no-op for the same events twice (backfill then live)", () => {
    const once = mergeEvents([], [ev(1), ev(2)]);
    expect(mergeEvents(once, [ev(2), ev(1)])).toEqual(once);
  });

  it("does not change its inputs", () => {
    const a = [ev(2)];
    const b = [ev(1)];
    mergeEvents(a, b);
    expect(a).toHaveLength(1);
    expect(b).toHaveLength(1);
  });
});

describe("gaps", () => {
  it("finds the end of the unbroken run from 1", () => {
    expect(contiguousSeq([])).toBe(0);
    expect(contiguousSeq([ev(1), ev(2), ev(3)])).toBe(3);
    expect(contiguousSeq([ev(1), ev(2), ev(4)])).toBe(2);
    expect(contiguousSeq([ev(2), ev(3)])).toBe(0);
  });

  it("sees a missing event before the newest one", () => {
    expect(hasGap([])).toBe(false);
    expect(hasGap([ev(1), ev(2)])).toBe(false);
    expect(hasGap([ev(1), ev(3)])).toBe(true);
    expect(hasGap([ev(5)])).toBe(true);
  });
});

describe("asEvent", () => {
  it("accepts an event and drops anything else", () => {
    expect(asEvent(ev(1))).not.toBeNull();
    expect(asEvent(null)).toBeNull();
    expect(asEvent("x")).toBeNull();
    expect(asEvent({ seq: "1", kind: "message", at: "t" })).toBeNull();
    expect(asEvent({ seq: 1, at: "t" })).toBeNull();
  });
});
