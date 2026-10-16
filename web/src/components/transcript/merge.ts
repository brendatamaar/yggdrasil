import type { Event as AgentEvent } from "../../api/gen/agents.gen";

/**
 * Merges events into the list kept so far: one event per `seq` (the newer
 * copy wins), ordered by `seq`. A live event and the same event from a
 * backfill therefore never show twice.
 */
export function mergeEvents(current: readonly AgentEvent[], incoming: readonly AgentEvent[]): AgentEvent[] {
  const bySeq = new Map<number, AgentEvent>();
  for (const e of current) bySeq.set(e.seq, e);
  for (const e of incoming) bySeq.set(e.seq, e);
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
}

/** The highest n such that events 1..n are all present. Seq starts at 1 without gaps. */
export function contiguousSeq(events: readonly AgentEvent[]): number {
  const seen = new Set(events.map((e) => e.seq));
  let n = 0;
  while (seen.has(n + 1)) n++;
  return n;
}

/** True when an event is missing before the newest one. */
export function hasGap(events: readonly AgentEvent[]): boolean {
  const last = events.at(-1);
  return last !== undefined && last.seq > contiguousSeq(events);
}

/** Reads an event off a WebSocket message; anything that isn't one is dropped. */
export function asEvent(data: unknown): AgentEvent | null {
  if (typeof data !== "object" || data === null) return null;
  const d = data as Record<string, unknown>;
  if (typeof d.seq !== "number" || typeof d.kind !== "string" || typeof d.at !== "string") return null;
  return data as AgentEvent;
}
