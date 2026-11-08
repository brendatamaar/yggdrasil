import { describe, expect, it } from "vitest";
import type { RepoEnvDTO } from "../../api/gen/api.gen";
import { draftProblem, formFrom, formStep, fromDraft, pathHint, portRange } from "./core";

const env: RepoEnvDTO = { repoId: "r1", profilePaths: [".env"], agentCopy: false, composeEnabled: false };

describe("pathHint", () => {
  it("accepts plain relative paths", () => {
    for (const p of [".env", "config/local.json", "a\\b.txt"]) expect(pathHint(p)).toBeNull();
  });
  it.each([
    ["", "Enter a path"],
    ["/etc/passwd", "absolute"],
    ["C:\\secrets\\.env", "absolute"],
    ["..\\x", "leave the repo"],
    ["a/../../b", "leave the repo"],
    [".git/config", ".git"],
    ["sub/.GIT/hooks", ".git"],
    ["*.env", "Wildcards"],
    ["a\u0000b", "control"],
  ])("refuses %j", (p, text) => {
    expect(pathHint(p)).toContain(text);
  });
  it("refuses a duplicate", () => {
    expect(pathHint(".env", [".env"])).toContain("already");
  });
});

describe("portRange", () => {
  it("formats a block, or nothing before one exists", () => {
    expect(portRange({ blockBase: 41000, blockSize: 10 })).toBe("41000-41009");
    expect(portRange({})).toBeNull();
  });
});

describe("env form reducer", () => {
  it("edits, then saves once and returns to a clean form from the saved data", () => {
    let r = formStep(formFrom(env), { tag: "edit", patch: { agentCopy: true } });
    expect(r.state).toMatchObject({ tag: "editing", dirty: true });
    r = formStep(r.state, { tag: "save" });
    expect(r.state.tag).toBe("saving");
    expect(r.commands).toHaveLength(1);
    // A second save while saving does nothing.
    expect(formStep(r.state, { tag: "save" }).commands).toEqual([]);
    r = formStep(r.state, { tag: "saved", env: { ...env, agentCopy: true } });
    expect(r.state).toMatchObject({ tag: "editing", dirty: false });
  });

  it("keeps the draft when a save fails and lets the person edit again", () => {
    let r = formStep(formFrom(env), { tag: "edit", patch: { profilePaths: [".env", ".env.local"] } });
    r = formStep(r.state, { tag: "save" });
    r = formStep(r.state, { tag: "saveFailed", message: "Nope." });
    expect(r.state).toMatchObject({ tag: "failed", message: "Nope." });
    expect(r.state.draft.profilePaths).toEqual([".env", ".env.local"]);
    expect(formStep(r.state, { tag: "edit", patch: { agentCopy: true } }).state.tag).toBe("editing");
  });

  it("does not save a draft with a problem", () => {
    const r = formStep(formFrom(env), { tag: "edit", patch: { composeEnabled: true } });
    expect(draftProblem(r.state.draft)).toContain("compose file");
    expect(formStep(r.state, { tag: "save" }).commands).toEqual([]);
  });

  it("reload never overwrites unsaved edits", () => {
    const edited = formStep(formFrom(env), { tag: "edit", patch: { agentCopy: true } }).state;
    expect(formStep(edited, { tag: "reload", env }).state).toBe(edited);
    expect(formStep(formFrom(env), { tag: "reload", env: { ...env, agentCopy: true } }).state.draft.agentCopy).toBe(true);
  });

  it("sends the compose file only when compose is on", () => {
    const d = { profilePaths: [], agentCopy: false, composeEnabled: false, composeFile: "docker-compose.yml" };
    expect(fromDraft("r1", d).composeFile).toBeUndefined();
    expect(fromDraft("r1", { ...d, composeEnabled: true }).composeFile).toBe("docker-compose.yml");
  });
});
