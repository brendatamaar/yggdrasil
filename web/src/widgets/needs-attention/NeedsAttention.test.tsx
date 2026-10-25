import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BuildDTO, RepoStatusDTO } from "../../api/gen/api.gen";
import { renderWidget } from "../../test/renderWidget";
import { stubApi, type Call } from "../../test/fakeApi";
import { NeedsAttention } from "./NeedsAttention";

const counts = { all: 3, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 0, problems: 0 };
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
const repos = (list: RepoStatusDTO[]) => ({
  counts,
  projects: [{ id: "p1", name: "acme-shop", path: "/work/acme-shop", counts, repos: list }],
});
const failedBuild = (id: string, repoId: string): BuildDTO => ({
  id,
  status: "failed",
  target: "repos",
  actor: "user",
  queuedAt: "2026-01-01T10:00:00Z",
  steps: [{ index: 0, repoId, name: "build", status: "failed", argv: ["go"], attempt: 1, logTruncated: false }],
  artifactsCollected: false,
  artifacts: [],
});

function handler(list: RepoStatusDTO[], builds: BuildDTO[] = []) {
  return (c: Call) => {
    if (c.path.startsWith("/api/repos?")) return { body: repos(list) };
    if (c.path.startsWith("/api/builds")) return { body: { builds } };
    return undefined;
  };
}

describe("Needs attention widget", () => {
  it("shows a loading state first", async () => {
    stubApi(handler([]));
    await renderWidget(NeedsAttention);
    expect(screen.getByText("Loading…")).toBeTruthy();
  });

  it("orders repos by urgency and explains each reason", async () => {
    stubApi(
      handler(
        [
          repo("r1", "shop-api", { unstaged: 2 }),
          repo("r2", "shop-web", { behind: 3 }),
          repo("r3", "shop-admin", { flags: ["diverged"] }),
          repo("r4", "shop-docs", { failedPipeline: true }),
        ],
        [failedBuild("b9", "r2")],
      ),
    );
    await renderWidget(NeedsAttention);
    await screen.findByText("Diverged from upstream");
    const names = screen.getAllByRole("link").filter((l) => l.getAttribute("href")?.startsWith("/repos/"));
    expect(names.map((l) => l.textContent)).toEqual(["shop-admin", "shop-docs", "shop-web", "shop-api"]);
    expect(screen.getByText("Latest build failed, 3 behind")).toBeTruthy();
    expect(screen.getByRole("link", { name: "View build" }).getAttribute("href")).toBe("/builds/b9");
    expect(screen.getByText("Uncommitted changes")).toBeTruthy();
  });

  it("explains an empty list", async () => {
    stubApi(handler([repo("r1", "shop-api")]));
    await renderWidget(NeedsAttention);
    expect(await screen.findByText("Nothing needs you right now.")).toBeTruthy();
  });

  it("shows an error", async () => {
    stubApi(() => ({ status: 500, body: { code: "internal", message: "Something went wrong.", hint: "Try again." } }));
    await renderWidget(NeedsAttention);
    expect((await screen.findByRole("alert")).textContent).toContain("Something went wrong.");
  });
});
