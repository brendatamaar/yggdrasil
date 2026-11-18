import { describe, expect, it } from "vitest";
import * as gen from "../../api/gen/api.gen";
import type { ActionOfferingDTO, RepoStatusDTO } from "../../api/gen/api.gen";
import { actionsForReasons, FALLBACK_KIND, kindTitle, reasonCodes, REASON_LABEL } from "./attention";
import { EMPTY_TEXT, viewKind, type EmptyKind } from "./empty";

const generatedReasons = Object.entries(gen)
  .filter(([k, v]) => k.startsWith("Reason") && typeof v === "string")
  .map(([, v]) => v);
const generatedKinds = Object.entries(gen)
  .filter(([k, v]) => /^Action[A-Z]/.test(k) && typeof v === "string" && !k.startsWith("ActionState") && !k.startsWith("ActionContext"))
  .map(([, v]) => v);

describe("attention reasons and actions", () => {
  it("every generated reason has a fallback action, a label, and the fallback is a known kind", () => {
    expect(generatedReasons.length).toBeGreaterThanOrEqual(7);
    for (const r of generatedReasons) {
      expect(FALLBACK_KIND[r], r).toBeDefined();
      expect(REASON_LABEL[r], r).toBeDefined();
      expect(generatedKinds).toContain(FALLBACK_KIND[r]);
    }
  });

  it("every generated action kind has a plain title", () => {
    for (const k of generatedKinds) expect(kindTitle(k)).not.toBe("Run action");
  });

  it("actionsForReasons never leaves a reason without an offering, with or without a catalogue", () => {
    const empty: ActionOfferingDTO[] = [];
    for (const cat of [undefined, empty]) {
      for (const g of actionsForReasons(generatedReasons, cat)) {
        expect(g.offerings.length).toBeGreaterThan(0);
        expect(g.fallback).toBe(true);
      }
    }
  });

  it("an unknown reason from a newer server still gets a safe Ask diagnosis", () => {
    const [g] = actionsForReasons(["something_new"], undefined);
    expect(g?.offerings[0]?.kind).toBe("diagnose_repo");
  });

  it("the catalogue's own offerings win over the fallback", () => {
    const o: ActionOfferingDTO = { kind: "fix_pipeline", reason: "failed_ci", title: "Fix in worktree", context: "available", mode: "code" };
    const [g] = actionsForReasons(["failed_ci"], [o]);
    expect(g).toEqual({ reason: "failed_ci", offerings: [o], fallback: false });
  });

  it("the unreadable fallback is marked as waiting for the folder", () => {
    const [g] = actionsForReasons(["unreadable"], undefined);
    expect(g?.offerings[0]?.waitingForFolder).toBe(true);
  });

  it("reasonCodes maps a repo to its reasons", () => {
    const base = {
      view: "fresh", flags: [], behind: 0, staged: 0, unstaged: 0, untracked: 0, conflicted: 0, failedPipeline: false,
    } as unknown as RepoStatusDTO;
    expect(reasonCodes(base, false)).toEqual([]);
    expect(reasonCodes({ ...base, view: "problem" }, false)).toEqual(["unreadable"]);
    expect(reasonCodes({ ...base, conflicted: 1, flags: ["diverged"], failedPipeline: true, behind: 1, unstaged: 2 }, true)).toEqual([
      "conflicts",
      "diverged",
      "failed_ci",
      "failed_build",
      "behind",
      "dirty",
    ]);
  });
});

describe("empty, pending and error are different views", () => {
  it("an error without data is an error, never empty", () => {
    expect(viewKind({ isPending: false, error: new Error("x"), count: undefined })).toBe("error");
  });
  it("pending without data is pending", () => {
    expect(viewKind({ isPending: true, error: null, count: undefined })).toBe("pending");
  });
  it("data with no items is empty, and with items is ready", () => {
    expect(viewKind({ isPending: false, error: null, count: 0 })).toBe("empty");
    expect(viewKind({ isPending: false, error: null, count: 3 })).toBe("ready");
  });
  it("every empty kind has its own wording", () => {
    const kinds: EmptyKind[] = ["NoProjects", "EmptyProject", "NoRemote", "NoCredentials", "NoIssues", "NoMRs", "NoBuilds", "NoTasks", "NoStandup"];
    expect(new Set(kinds.map((k) => EMPTY_TEXT[k])).size).toBe(kinds.length);
  });
});
