import { describe, expect, it } from "vitest";
import type { StandupResponse } from "../../api/gen/api.gen";
import { stepStandup, type StandupInput, type StandupState } from "./core";

// Properties of the standup reducer (AU-02, UI-03): reading and polishing are separate, explicit
// steps; a stale answer never changes the screen; a polish is only ever sent for the text shown.

function rng(seed: number) {
  let s = seed;
  return () => (s = (s * 1103515245 + 12345) % 2147483648) / 2147483648;
}

const range = { from: "2026-03-01T00:00:00Z", to: "2026-03-02T00:00:00Z" };
const response = (phase: string, text: string): StandupResponse =>
  ({ phase, message: "", from: range.from, to: range.to, text, commits: [], mrs: [], builds: [] }) as unknown as StandupResponse;

describe("standup reducer properties", () => {
  it("over random inputs: stale attempts change nothing, polish needs shown text, reads start only from a request", () => {
    let polishes = 0;
    let reads = 0;
    for (let seed = 1; seed <= 300; seed++) {
      const r = rng(seed);
      const pick = <T,>(xs: readonly T[]): T => xs[Math.floor(r() * xs.length)] as T;
      let state: StandupState = { tag: "empty" };
      for (let i = 0; i < 80; i++) {
        const attempt = pick([1, 2, 3]);
        const input: StandupInput = pick<StandupInput>([
          { tag: "requested", range, attempt },
          { tag: "sourcesRead", attempt, response: response(pick(["ready", "partial", "failed"]), pick(["", "a commit", "  "])) },
          { tag: "readFailed", attempt, reason: "x" },
          { tag: "cancelled", attempt },
          { tag: "polishRequested", requestId: pick(["k1", "k2"]) },
        ]);
        const before = state;
        const [after, cmds] = stepStandup(state, input);
        state = after;

        if (input.tag === "sourcesRead" || input.tag === "readFailed" || input.tag === "cancelled") {
          const stale = before.tag !== "loading" || before.attempt !== input.attempt;
          if (stale) {
            expect(after).toBe(before);
            expect(cmds).toHaveLength(0);
          }
        }
        for (const c of cmds) {
          if (c.tag === "readSources") {
            reads++;
            expect(input.tag).toBe("requested");
          }
          if (c.tag === "submitPolish") {
            polishes++;
            expect(input.tag).toBe("polishRequested");
            expect(before.tag === "ready" || before.tag === "partial").toBe(true);
            if (before.tag === "ready" || before.tag === "partial") expect(before.summary.text.trim()).not.toBe("");
            expect(after).toBe(before); // polishing reads nothing and changes no screen state
          }
        }
        // At most one read is in flight: a new request while loading cancels the old attempt.
        if (input.tag === "requested" && before.tag === "loading" && cmds.some((c) => c.tag === "readSources")) {
          expect(cmds.some((c) => c.tag === "cancelRead" && c.attempt === before.attempt)).toBe(true);
        }
      }
    }
    expect(reads).toBeGreaterThan(50);
    expect(polishes).toBeGreaterThan(0);
  });
});
