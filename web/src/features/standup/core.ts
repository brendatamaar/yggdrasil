// Pure decisions of the standup view (docs/design/phase-6-standup.md, AU-02/UI-03). No IO,
// no clock: dates and RequestIDs arrive as inputs. A refresh only reads; the one way to an
// agent is the explicit polish input.
import type { StandupResponse } from "../../api/gen/api.gen";

export interface DayRange {
  /** RFC 3339, UTC, inclusive. */
  from: string;
  /** RFC 3339, UTC, exclusive. */
  to: string;
}

export const MAX_RANGE_DAYS = 31;

const ymd = /^(\d{4})-(\d{2})-(\d{2})$/;

function parseDay(s: string): { y: number; m: number; d: number } | null {
  const m = ymd.exec(s);
  if (!m) return null;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const probe = new Date(Date.UTC(y, mo - 1, d));
  if (probe.getUTCFullYear() !== y || probe.getUTCMonth() !== mo - 1 || probe.getUTCDate() !== d) return null;
  return { y, m: mo, d };
}

export type RangeResult = { ok: true; range: DayRange; days: number } | { ok: false; message: string };

/**
 * A range from two local calendar days, both inclusive. The server still validates it; this
 * refuses what cannot be right before any request is made. Day counting ignores DST.
 */
export function rangeFromDays(fromDay: string, toDay: string): RangeResult {
  const a = parseDay(fromDay);
  const b = parseDay(toDay);
  if (!a || !b) return { ok: false, message: "Pick a valid first and last day." };
  const days = (Date.UTC(b.y, b.m - 1, b.d) - Date.UTC(a.y, a.m - 1, a.d)) / 86_400_000 + 1;
  if (days < 1) return { ok: false, message: "The last day is before the first day." };
  if (days > MAX_RANGE_DAYS) return { ok: false, message: `Pick at most ${String(MAX_RANGE_DAYS)} days.` };
  return {
    ok: true,
    days,
    range: {
      from: new Date(a.y, a.m - 1, a.d).toISOString(),
      to: new Date(b.y, b.m - 1, b.d + 1).toISOString(),
    },
  };
}

/** `YYYY-MM-DD` of a local date. */
export const dayString = (d: Date): string =>
  `${String(d.getFullYear())}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

// --- State machine -----------------------------------------------------------

export type StandupState =
  | { tag: "empty" }
  | { tag: "loading"; attempt: number; range: DayRange }
  | { tag: "ready"; summary: StandupResponse }
  | { tag: "partial"; summary: StandupResponse }
  | { tag: "failed"; reason: string; range: DayRange };

export type StandupInput =
  | { tag: "requested"; range: DayRange; attempt: number }
  | { tag: "sourcesRead"; attempt: number; response: StandupResponse }
  | { tag: "readFailed"; attempt: number; reason: string }
  | { tag: "cancelled"; attempt: number }
  | { tag: "polishRequested"; requestId: string };

export type StandupCommand =
  | { tag: "readSources"; attempt: number; range: DayRange }
  | { tag: "cancelRead"; attempt: number }
  | { tag: "submitPolish"; requestId: string; range: DayRange };

type Step = [StandupState, StandupCommand[]];

/** The range shown by a state that has one. */
export function rangeOf(s: StandupState): DayRange | undefined {
  switch (s.tag) {
    case "empty":
      return undefined;
    case "loading":
    case "failed":
      return s.range;
    default:
      return { from: s.summary.from, to: s.summary.to };
  }
}

export function stepStandup(state: StandupState, input: StandupInput): Step {
  const same: Step = [state, []];
  switch (input.tag) {
    case "requested": {
      // A new request replaces the one in flight: the older answer can never win.
      const cancel: StandupCommand[] = state.tag === "loading" ? [{ tag: "cancelRead", attempt: state.attempt }] : [];
      if (state.tag === "loading" && input.attempt <= state.attempt) return same;
      return [
        { tag: "loading", attempt: input.attempt, range: input.range },
        [...cancel, { tag: "readSources", attempt: input.attempt, range: input.range }],
      ];
    }
    case "sourcesRead": {
      if (state.tag !== "loading" || state.attempt !== input.attempt) return same;
      const r = input.response;
      // The server's own phase decides: an empty summary is a result, not a failure.
      if (r.phase === "failed") return [{ tag: "failed", reason: r.message || "No source could be read.", range: state.range }, []];
      if (r.phase === "partial") return [{ tag: "partial", summary: r }, []];
      return [{ tag: "ready", summary: r }, []];
    }
    case "readFailed": {
      if (state.tag !== "loading" || state.attempt !== input.attempt) return same;
      return [{ tag: "failed", reason: input.reason, range: state.range }, []];
    }
    case "cancelled": {
      if (state.tag !== "loading" || state.attempt !== input.attempt) return same;
      return [{ tag: "empty" }, [{ tag: "cancelRead", attempt: input.attempt }]];
    }
    case "polishRequested": {
      if (state.tag !== "ready" && state.tag !== "partial") return same;
      if (state.summary.text.trim() === "") return same;
      return [state, [{ tag: "submitPolish", requestId: input.requestId, range: { from: state.summary.from, to: state.summary.to } }]];
    }
  }
}

/** True when a summary has no events at all: the explicit NoStandup state. */
export const isEmptySummary = (s: StandupResponse): boolean =>
  s.commits.length === 0 && s.mrs.length === 0 && s.builds.length === 0;
