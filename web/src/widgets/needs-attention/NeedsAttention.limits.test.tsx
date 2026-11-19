import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BuildDTO, RepoStatusDTO } from "../../api/gen/api.gen";
import { stubApi, type Call } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { NeedsAttention } from "./NeedsAttention";

// Mixed reasons and the top-eight cut (phase-6-ui.md): a repo with several reasons shows an action
// for each of them, and the cut never hides a reason of a repo that is shown.

const counts = { all: 10, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 0, problems: 0 };
const repo = (id: string, name: string, over: Partial<RepoStatusDTO> = {}): RepoStatusDTO => ({
  id,
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
const body = (list: RepoStatusDTO[]) => ({ counts, projects: [{ id: "p1", name: "acme-shop", path: "/work/acme-shop", counts, repos: list }] });
const failedBuild = (repoId: string): BuildDTO => ({
  id: "b1",
  status: "failed",
  target: "repos",
  actor: "user",
  queuedAt: "2026-01-01T10:00:00Z",
  steps: [{ index: 0, repoId, name: "build", status: "failed", argv: ["go"], attempt: 1, logTruncated: false }],
  artifactsCollected: false,
  artifacts: [],
});
const handler = (list: RepoStatusDTO[], builds: BuildDTO[] = []) => (c: Call) => {
  if (c.path.startsWith("/api/repos?")) return { body: body(list) };
  if (c.path.startsWith("/api/builds")) return { body: { builds } };
  return undefined;
};

describe("Needs attention: mixed reasons and the top-eight cut", () => {
  it("a repo with four reasons shows a different action for each", async () => {
    stubApi(handler([repo("r1", "shop-api", { conflicted: 1, behind: 2, unstaged: 3 })], [failedBuild("r1")]));
    await renderWidget(NeedsAttention);
    const list = await screen.findByRole("list", { name: "Actions for shop-api" });
    const reasons = within(list)
      .getAllByRole("listitem")
      .map((li) => li.getAttribute("data-reason"));
    expect(reasons).toEqual(expect.arrayContaining(["conflicts", "failed_build", "behind", "dirty"]));
  });

  it("shows the eight most urgent repos and every one of them still has its actions", async () => {
    // Ten dirty repos plus two unreadable ones: the unreadable ones are more urgent and must be inside the cut.
    const dirty = Array.from({ length: 10 }, (_, i) => repo(`d${String(i)}`, `shop-dirty-${String(i).padStart(2, "0")}`, { unstaged: 1 }));
    const broken = ["shop-broken-a", "shop-broken-b"].map((n, i) => repo(`b${String(i)}`, n, { view: "problem", problem: "missing", problemMessage: "The folder is missing." }));
    stubApi(handler([...dirty, ...broken]));
    await renderWidget(NeedsAttention);
    await screen.findByRole("list", { name: "Actions for shop-broken-a" });
    await screen.findByRole("list", { name: "Actions for shop-broken-b" });
    const lists = screen.getAllByRole("list").filter((l) => (l.getAttribute("aria-label") ?? "").startsWith("Actions for "));
    expect(lists).toHaveLength(8);
    for (const l of lists) expect(within(l).getAllByRole("button").length).toBeGreaterThan(0);
    // The two least urgent (by name) are cut, not the unreadable ones.
    expect(screen.queryByRole("list", { name: "Actions for shop-dirty-09" })).toBeNull();
    expect(screen.queryByRole("list", { name: "Actions for shop-dirty-08" })).toBeNull();
  });

  it("a clean repo is not listed and offers nothing", async () => {
    stubApi(handler([repo("r1", "shop-api"), repo("r2", "shop-web", { unstaged: 1 })]));
    await renderWidget(NeedsAttention);
    await screen.findByRole("list", { name: "Actions for shop-web" });
    expect(screen.queryByRole("list", { name: "Actions for shop-api" })).toBeNull();
  });
});
