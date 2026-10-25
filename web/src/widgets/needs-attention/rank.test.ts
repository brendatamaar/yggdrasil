import { describe, expect, it } from "vitest";
import type { BuildDTO, RepoStatusDTO } from "../../api/gen/api.gen";
import { failedBuilds, rankRepos, reasonsFor } from "./rank";

const repo = (name: string, over: Partial<RepoStatusDTO> = {}): RepoStatusDTO => ({
  id: name,
  projectId: "p1",
  name,
  relPath: name,
  path: `/work/acme-shop/${name}`,
  trusted: false,
  refreshing: false,
  view: "fresh",
  flags: [],
  branch: "main",
  detached: false,
  upstream: "origin/main",
  hasUpstream: true,
  ahead: 0,
  behind: 0,
  staged: 0,
  unstaged: 0,
  untracked: 0,
  conflicted: 0,
  failedPipeline: false,
  ...over,
});
const build = (id: string, status: string, repoIds: string[]): BuildDTO => ({
  id,
  status,
  target: "repos",
  actor: "user",
  queuedAt: "2026-01-01T10:00:00Z",
  steps: repoIds.map((repoId, index) => ({ index, repoId, name: "build", status, argv: ["go"], attempt: 1, logTruncated: false })),
  artifactsCollected: false,
  artifacts: [],
});

describe("reasonsFor", () => {
  it("has no reasons for a clean repo", () => {
    expect(reasonsFor(repo("a"), false)).toEqual({ urgency: 99, reasons: [] });
  });
  it.each([
    ["problem with message", repo("a", { view: "problem", problemMessage: "Folder is missing" }), false, 0, "Folder is missing"],
    ["problem without message", repo("a", { view: "problem" }), false, 0, "Cannot read this repo"],
    ["conflicted files", repo("a", { conflicted: 2 }), false, 0, "Has conflicts"],
    ["conflicts flag", repo("a", { flags: ["conflicts"] }), false, 0, "Has conflicts"],
    ["diverged", repo("a", { flags: ["diverged"] }), false, 1, "Diverged from upstream"],
    ["failed pipeline", repo("a", { failedPipeline: true }), false, 2, "Pipeline failed"],
    ["failed build", repo("a"), true, 3, "Latest build failed"],
    ["behind", repo("a", { behind: 3 }), false, 4, "3 behind"],
    ["staged", repo("a", { staged: 1 }), false, 5, "Uncommitted changes"],
    ["unstaged", repo("a", { unstaged: 1 }), false, 5, "Uncommitted changes"],
    ["untracked", repo("a", { untracked: 1 }), false, 5, "Uncommitted changes"],
  ])("%s", (_n, r, failed, urgency, text) => {
    expect(reasonsFor(r, failed)).toEqual({ urgency, reasons: [text] });
  });
  it("takes the lowest urgency and keeps every reason in rule order", () => {
    const r = repo("a", { failedPipeline: true, behind: 1, unstaged: 1 });
    expect(reasonsFor(r, false)).toEqual({ urgency: 2, reasons: ["Pipeline failed", "1 behind", "Uncommitted changes"] });
  });
});

describe("failedBuilds", () => {
  it("counts only the newest finished build per repo", () => {
    const m = failedBuilds([build("b3", "succeeded", ["a"]), build("b2", "running", ["a"]), build("b1", "failed", ["a", "b"])]);
    expect(m.get("a")).toBeUndefined();
    expect(m.get("b")).toBe("b1");
  });
  it("ignores queued and running builds", () => {
    expect(failedBuilds([build("b1", "queued", ["a"]), build("b2", "running", ["a"])]).size).toBe(0);
  });
});

describe("rankRepos", () => {
  it("drops clean repos and orders by urgency", () => {
    const out = rankRepos([repo("clean"), repo("dirty", { unstaged: 1 }), repo("conf", { conflicted: 1 }), repo("pipe", { failedPipeline: true })], []);
    expect(out.map((i) => i.repo.name)).toEqual(["conf", "pipe", "dirty"]);
  });
  it("breaks ties by name", () => {
    const out = rankRepos([repo("shop-web", { behind: 1 }), repo("shop-api", { behind: 1 }), repo("shop-admin", { behind: 1 })], []);
    expect(out.map((i) => i.repo.name)).toEqual(["shop-admin", "shop-api", "shop-web"]);
  });
  it("attaches the failed build id and raises urgency", () => {
    const out = rankRepos([repo("shop-api", { behind: 1 })], [build("b9", "failed", ["shop-api"])]);
    expect(out[0]).toMatchObject({ urgency: 3, failedBuildId: "b9" });
  });
});
