import { describe, expect, it } from "vitest";
import { stepConfirm, type ConfirmInput, type ConfirmState } from "./core";

// TestPropConfirmUsesShownHash (docs/design/phase-6-ui.md): whatever order inputs arrive in, a
// confirmation is only ever sent with the exact id and hash the person was shown, and never after
// the shown hash stopped matching the server's.

function rng(seed: number) {
  let s = seed;
  return () => (s = (s * 1103515245 + 12345) % 2147483648) / 2147483648;
}

describe("confirm flow properties", () => {
  it("every send carries the id and hash of the last review that was opened and is still current", () => {
    let sends = 0;
    for (let seed = 1; seed <= 300; seed++) {
      const r = rng(seed);
      const pick = <T,>(xs: readonly T[]): T => xs[Math.floor(r() * xs.length)] as T;
      let state: ConfirmState = { tag: "idle" };
      // What the person was shown: set by every accepted `open`.
      let shown: { id: string; hash: string } | undefined;
      // The server's hash for the proposal, moved by `seen` inputs.
      let serverHash: string | undefined;
      for (let i = 0; i < 80; i++) {
        const id = pick(["p1", "p2"]);
        const hash = pick(["h1", "h2", "h3"]);
        const input: ConfirmInput = pick<ConfirmInput>([
          { tag: "open", id, hash },
          { tag: "seen", id, hash, pending: r() < 0.8 },
          { tag: "confirm" },
          { tag: "confirm" },
          { tag: "cancel" },
          { tag: "succeeded" },
          { tag: "conflict" },
          { tag: "failed", message: "x" },
        ]);
        const before = state;
        const [after, cmds] = stepConfirm(state, input);
        state = after;
        if (input.tag === "open" && before.tag !== "sending") {
          shown = { id: input.id, hash: input.hash };
          serverHash = undefined;
        }
        if (input.tag === "seen" && before.tag === "reviewing" && shown?.id === input.id) serverHash = input.hash;
        for (const c of cmds) {
          if (c.tag !== "send") continue;
          sends++;
          expect(input.tag).toBe("confirm");
          expect(before.tag).toBe("reviewing");
          expect(c).toMatchObject({ id: shown?.id, hash: shown?.hash });
          // The server never reported a different hash for what was shown while it was open.
          if (serverHash !== undefined) expect(serverHash).toBe(shown?.hash);
        }
        // A "changed" flow can only leave through a fresh open: confirm sends nothing from it.
        if (before.tag === "changed" && input.tag === "confirm") expect(cmds).toHaveLength(0);
        // Never two sends for one review: sending ignores a second confirm.
        if (before.tag === "sending" && input.tag === "confirm") expect(cmds).toHaveLength(0);
      }
    }
    expect(sends).toBeGreaterThan(50);
  });

  it("a hash that moved while reviewing always ends in 'changed' with nothing sent", () => {
    for (const moved of ["h2", "h3", "other"]) {
      const [s1] = stepConfirm({ tag: "idle" }, { tag: "open", id: "p1", hash: "h1" });
      const [s2, c2] = stepConfirm(s1, { tag: "seen", id: "p1", hash: moved, pending: true });
      expect(s2).toEqual({ tag: "changed", id: "p1" });
      expect(c2).toHaveLength(0);
      const [s3, c3] = stepConfirm(s2, { tag: "confirm" });
      expect(s3).toEqual(s2);
      expect(c3).toHaveLength(0);
    }
  });
});
