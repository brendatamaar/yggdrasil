import { describe, expect, it } from "vitest";
import {
  findMention,
  insertMention,
  invalidReason,
  isValidModelName,
  stepDraft,
  stepSubmit,
  switchTargets,
  type FormValues,
} from "./core";

const ok: FormValues = {
  prompt: "p",
  mode: "ask",
  scope: "repo",
  projectId: "prj",
  repoId: "rep",
  agent: "mock",
  agentAvailable: true,
  model: "",
};

describe("core", () => {
  it("model-name rule", () => {
    for (const good of ["a", "claude-3.5:x", "A".repeat(64)]) expect(isValidModelName(good)).toBe(true);
    for (const bad of ["", "a b", "a/b", "A".repeat(65), "a_b", "é"]) expect(isValidModelName(bad)).toBe(false);
  });

  it("explains why the form is invalid", () => {
    expect(invalidReason(ok)).toBeNull();
    expect(invalidReason({ ...ok, prompt: " " })).toMatch(/Write/);
    expect(invalidReason({ ...ok, repoId: "" })).toMatch(/repo/);
    expect(invalidReason({ ...ok, repoId: "", scope: "project" })).toBeNull();
    expect(invalidReason({ ...ok, agentAvailable: false })).toMatch(/agent/);
    expect(invalidReason({ ...ok, model: "no no" })).toMatch(/Model/);
  });

  it("switch targets", () => {
    expect(switchTargets("ask", "code")).toEqual(["code"]);
    expect(switchTargets("ask", undefined)).toEqual([]);
    expect(switchTargets("code", "code")).toEqual(["ask"]);
    expect(switchTargets("project", "project")).toEqual(["ask"]);
  });

  it("optIn needs the dialog first, for risky modes only", () => {
    const [s1, c1] = stepSubmit({ tag: "editing" }, { tag: "submit", mode: "current_branch", acknowledged: false });
    expect(s1).toEqual({ tag: "confirming", mode: "current_branch" });
    expect(c1).toEqual([]);
    const [, c2] = stepSubmit(s1, { tag: "confirm" });
    expect(c2).toContainEqual({ tag: "create", optIn: true });
    expect(stepSubmit({ tag: "editing" }, { tag: "submit", mode: "code", acknowledged: false })[1]).toEqual([{ tag: "create", optIn: false }]);
    // Cancel never creates; a second submit while submitting is ignored.
    expect(stepSubmit(s1, { tag: "cancel" })).toEqual([{ tag: "editing" }, []]);
    expect(stepSubmit({ tag: "submitting" }, { tag: "submit", mode: "ask", acknowledged: false })[1]).toEqual([]);
  });

  it("a late draft answer after cancel is dropped", () => {
    const [loading] = stepDraft({ tag: "idle" }, { tag: "start", requestId: 1 });
    const [idle] = stepDraft(loading, { tag: "cancel" });
    expect(stepDraft(idle, { tag: "done", requestId: 1 })).toEqual([{ tag: "idle" }, []]);
  });

  it("finds and replaces the @ token", () => {
    expect(findMention("see @sh", 7)).toEqual({ start: 4, query: "sh" });
    expect(findMention("mail a@b", 8)).toBeNull();
    expect(findMention("@a b", 4)).toBeNull();
    const t = findMention("see @sh now", 7);
    expect(t).not.toBeNull();
    if (t) expect(insertMention("see @sh now", 7, t, "shop-api", "a.ts")).toEqual({ text: "see @shop-api/a.ts  now", caret: 19 });
  });
});
