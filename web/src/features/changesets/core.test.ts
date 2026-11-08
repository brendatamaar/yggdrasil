import { describe, expect, it } from "vitest";
import {
  canSubmit,
  moveRepo,
  runItems,
  stepLanding,
  stepRun,
  type LandingFlow,
  type LandingInput,
  type RunCommand,
  type RunFlow,
  type RunInput,
  type RunItem,
} from "./core";

// Small deterministic generator so the property tests are reproducible.
function rng(seed: number) {
  let s = seed;
  return () => {
    s = (s * 1664525 + 1013904223) % 4294967296;
    return s / 4294967296;
  };
}

const items: RunItem[] = [
  { proposalId: "p1", repoId: "r1", kind: "merge_request", hash: "h1" },
  { proposalId: "p2", repoId: "r2", kind: "merge_request", hash: "h2" },
];
const warn = { code: "dependent_before_dependency", message: "r2 needs r1 first." };
const all = { kind: "all" } as const;

describe("moveRepo", () => {
  it("always returns a permutation of the input", () => {
    const rand = rng(1);
    let order = ["a", "b", "c", "d"];
    for (let n = 0; n < 200; n++) {
      order = moveRepo(order, Math.floor(rand() * 6) - 1, rand() < 0.5 ? -1 : 1);
      expect([...order].sort()).toEqual(["a", "b", "c", "d"]);
    }
  });
  it("moves one step and ignores the edges", () => {
    expect(moveRepo(["a", "b", "c"], 1, -1)).toEqual(["b", "a", "c"]);
    expect(moveRepo(["a", "b", "c"], 0, -1)).toEqual(["a", "b", "c"]);
    expect(moveRepo(["a", "b", "c"], 2, 1)).toEqual(["a", "b", "c"]);
  });
});

describe("runItems", () => {
  const cs = {
    order: ["r2", "r1"],
    members: [
      { proposalId: "p1", repoId: "r1", kind: "merge_request", status: "pending", hash: "h1" },
      { proposalId: "p2", repoId: "r2", kind: "merge_request", status: "pending", hash: "h2" },
      { proposalId: "p3", repoId: "r1", kind: "merge_local", status: "pending", hash: "h3" },
      { proposalId: "p4", repoId: "r1", kind: "push", status: "executed", hash: "h4" },
    ],
  };
  it("takes pending, non-landing members in merge order", () => {
    expect(runItems(cs, all).map((i) => i.proposalId)).toEqual(["p2", "p1"]);
  });
  it("limits to one repo", () => {
    expect(runItems(cs, { kind: "repo", repoId: "r1" }).map((i) => i.proposalId)).toEqual(["p1"]);
  });
});

describe("confirm-run flow", () => {
  const reviewing = (warnings = [warn]): RunFlow => {
    let [s] = stepRun({ tag: "idle" }, { tag: "open", scope: all, items });
    [s] = stepRun(s, { tag: "preflighted", warnings });
    return s;
  };

  it("open asks for a preflight and nothing else", () => {
    const [s, cmds] = stepRun({ tag: "idle" }, { tag: "open", scope: all, items });
    expect(s.tag).toBe("loading");
    expect(cmds).toEqual([{ tag: "preflight", scope: all }]);
  });

  it("a warning blocks submit until acknowledged, then the request has the seen hashes and the ack", () => {
    const s = reviewing();
    expect(canSubmit(s)).toBe(false);
    expect(stepRun(s, { tag: "submit" })).toEqual([s, []]);
    const [acked] = stepRun(s, { tag: "ack", code: warn.code, on: true });
    const [sending, cmds] = stepRun(acked, { tag: "submit" });
    expect(sending.tag).toBe("sending");
    expect(cmds).toEqual([{ tag: "confirm", request: { scope: all, seen: { p1: "h1", p2: "h2" }, ack: [warn.code] } }]);
  });

  it("without warnings no ack is sent", () => {
    const [, cmds] = stepRun(reviewing([]), { tag: "submit" });
    expect(cmds).toEqual([{ tag: "confirm", request: { scope: all, seen: { p1: "h1", p2: "h2" } } }]);
  });

  it("un-acknowledging blocks again", () => {
    let [s] = stepRun(reviewing(), { tag: "ack", code: warn.code, on: true });
    [s] = stepRun(s, { tag: "ack", code: warn.code, on: false });
    expect(canSubmit(s)).toBe(false);
  });

  it("a failed preflight cannot be submitted; only a retry is offered", () => {
    let [s] = stepRun({ tag: "idle" }, { tag: "open", scope: all, items });
    [s] = stepRun(s, { tag: "preflightFailed", message: "no" });
    expect(s.tag).toBe("preflightFailed");
    expect(canSubmit(s)).toBe(false);
    expect(stepRun(s, { tag: "submit" })).toEqual([s, []]);
    const [again, cmds] = stepRun(s, { tag: "retry" });
    expect(again.tag).toBe("loading");
    expect(cmds).toEqual([{ tag: "preflight", scope: all }]);
  });

  it("a changed set or hash goes to changed and sends nothing", () => {
    const [s] = stepRun(reviewing([]), { tag: "seen", items: items.map((it, i) => (i === 0 ? { ...it, hash: "other" } : it)) });
    expect(s.tag).toBe("changed");
    expect(stepRun(s, { tag: "submit" })[1]).toEqual([]);
  });

  it("a refusal returns to reviewing with the reason and keeps the acks", () => {
    let [s] = stepRun(reviewing(), { tag: "ack", code: warn.code, on: true });
    [s] = stepRun(s, { tag: "submit" });
    [s] = stepRun(s, { tag: "rejected", conflict: false, message: "Another run is going." });
    expect(s).toMatchObject({ tag: "reviewing", error: "Another run is going.", acked: [warn.code] });
  });

  it("cannot be cancelled or re-opened while sending", () => {
    let [s] = stepRun(reviewing([]), { tag: "submit" });
    expect(stepRun(s, { tag: "cancel" })[0].tag).toBe("sending");
    expect(stepRun(s, { tag: "open", scope: all, items })[0].tag).toBe("sending");
    [s] = stepRun(s, { tag: "accepted" });
    expect(s.tag).toBe("idle");
  });

  it("property: over random inputs at most one request is emitted per sending phase and only from reviewing with all acks", () => {
    const rand = rng(7);
    const gen = (): RunInput => {
      const k = Math.floor(rand() * 10);
      switch (k) {
        case 0: return { tag: "open", scope: all, items };
        case 1: return { tag: "preflighted", warnings: rand() < 0.5 ? [warn] : [] };
        case 2: return { tag: "preflightFailed", message: "x" };
        case 3: return { tag: "ack", code: warn.code, on: rand() < 0.5 };
        case 4: return { tag: "seen", items: rand() < 0.8 ? items : [] };
        case 5: return { tag: "submit" };
        case 6: return { tag: "accepted" };
        case 7: return { tag: "rejected", conflict: rand() < 0.3, message: "no" };
        case 8: return rand() < 0.3 ? { tag: "retry" } : { tag: "cancel" };
        default: return { tag: "submit" };
      }
    };
    for (let run = 0; run < 100; run++) {
      let s: RunFlow = { tag: "idle" };
      let outstanding = 0;
      for (let n = 0; n < 60; n++) {
        const input = gen();
        const before = s;
        const [next, cmds] = stepRun(s, input);
        if (next.tag === "preflightFailed") expect(canSubmit(next)).toBe(false);
        const confirms = cmds.filter((c: RunCommand) => c.tag === "confirm");
        if (confirms.length > 0) {
          expect(input.tag).toBe("submit");
          expect(before.tag).toBe("reviewing");
          expect(canSubmit(before)).toBe(true);
          expect(next.tag).toBe("sending");
          outstanding++;
        }
        expect(confirms.length).toBeLessThanOrEqual(1);
        if (next.tag !== "sending" && before.tag === "sending") outstanding--;
        expect(outstanding).toBeLessThanOrEqual(1);
        s = next;
      }
    }
  });
});

describe("landing flow", () => {
  const form = (kind: "merge_local" | "remove_worktree"): LandingFlow => stepLanding({ tag: "closed" }, { tag: "open", kind, worktreeId: "w1", title: "" })[0];

  it("submit emits exactly one propose command and then ignores further submits", () => {
    const [s, cmds] = stepLanding(form("merge_local"), { tag: "submit" });
    expect(s.tag).toBe("sending");
    expect(cmds).toEqual([{ tag: "propose", kind: "merge_local", worktreeId: "w1" }]);
    expect(stepLanding(s, { tag: "submit" })).toEqual([s, []]);
  });
  it("carries a typed title for the merge only", () => {
    let [s] = stepLanding(form("merge_local"), { tag: "type", title: "  Land cart fix " });
    expect(stepLanding(s, { tag: "submit" })[1]).toEqual([{ tag: "propose", kind: "merge_local", worktreeId: "w1", title: "Land cart fix" }]);
    [s] = stepLanding(form("remove_worktree"), { tag: "type", title: "x" });
    expect(stepLanding(s, { tag: "submit" })[1]).toEqual([{ tag: "propose", kind: "remove_worktree", worktreeId: "w1" }]);
  });
  it("a failure returns to the form with the reason", () => {
    let [s] = stepLanding(form("remove_worktree"), { tag: "submit" });
    [s] = stepLanding(s, { tag: "failed", message: "No." });
    expect(s).toMatchObject({ tag: "form", error: "No." });
  });
  it("property: any input sequence emits only propose commands, and never more than one per sending phase", () => {
    const rand = rng(3);
    const gen = (): LandingInput => {
      switch (Math.floor(rand() * 7)) {
        case 0: return { tag: "open", kind: rand() < 0.5 ? "merge_local" : "remove_worktree", worktreeId: "w1", title: "" };
        case 1: return { tag: "type", title: "t" };
        case 2: return { tag: "created", proposalId: "p" };
        case 3: return { tag: "failed", message: "m" };
        case 4: return { tag: "close" };
        default: return { tag: "submit" };
      }
    };
    for (let run = 0; run < 100; run++) {
      let s: LandingFlow = { tag: "closed" };
      for (let n = 0; n < 60; n++) {
        const [next, cmds] = stepLanding(s, gen());
        expect(cmds.length).toBeLessThanOrEqual(1);
        cmds.forEach((c) => {
          expect(c.tag).toBe("propose");
          expect(s.tag).toBe("form");
        });
        s = next;
      }
    }
  });
});
