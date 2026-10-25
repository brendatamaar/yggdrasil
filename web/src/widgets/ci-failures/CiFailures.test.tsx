import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWidget } from "../../test/renderWidget";
import { stubApi, type Call } from "../../test/fakeApi";
import { CiFailures } from "./CiFailures";

const counts = { all: 1, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 1, problems: 0 };
const repo = (failed: boolean) => ({
  id: "r1",
  projectId: "p1",
  name: "shop-api",
  relPath: "shop-api",
  path: "/work/acme-shop/shop-api",
  trusted: false,
  refreshing: false,
  view: "fresh" as const,
  flags: [],
  branch: "feature/cart",
  detached: false,
  upstream: "origin/main",
  hasUpstream: true,
  ahead: 0,
  behind: 0,
  staged: 0,
  unstaged: 0,
  untracked: 0,
  conflicted: 0,
  failedPipeline: failed,
});
const repos = (failed: boolean) => ({
  counts,
  projects: [{ id: "p1", name: "acme-shop", path: "/work/acme-shop", counts, repos: failed ? [repo(true)] : [] }],
});
const host = (url: string) => ({
  linked: true,
  pipeline: { status: "failed", url, updatedAt: "2026-01-01T10:00:00Z" },
  links: { repo: "https://git.acme.dev/acme-shop/shop-api", mrList: "x", pipelines: "https://git.acme.dev/acme-shop/shop-api/-/pipelines" },
  phase: "idle",
  credentialsInRemote: false,
});

function handler(failed: boolean, pipelineUrl = "https://git.acme.dev/acme-shop/shop-api/-/pipelines/7") {
  return (c: Call) => {
    if (c.path.startsWith("/api/repos?")) return { body: repos(failed) };
    if (c.path === "/api/repos/r1/host") return { body: host(pipelineUrl) };
    return undefined;
  };
}

describe("CI failures widget", () => {
  it("shows a loading state first", async () => {
    stubApi(handler(true));
    await renderWidget(CiFailures);
    expect(screen.getByText("Loading…")).toBeTruthy();
  });

  it("lists failing repos with a link to the pipeline", async () => {
    stubApi(handler(true));
    await renderWidget(CiFailures);
    expect(await screen.findByRole("link", { name: "shop-api" })).toBeTruthy();
    const run = await screen.findByRole("link", { name: "View pipeline" });
    expect(run.getAttribute("href")).toContain("/pipelines/7");
    expect(run.getAttribute("rel")).toContain("noopener");
  });

  it("does not link an unsafe pipeline URL", async () => {
    stubApi(handler(true, "javascript:alert(1)"));
    await renderWidget(CiFailures);
    expect(await screen.findByText("View pipeline")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "View pipeline" })).toBeNull();
  });

  it("explains an empty list", async () => {
    stubApi(handler(false));
    await renderWidget(CiFailures);
    expect(await screen.findByText("No failing pipelines.")).toBeTruthy();
  });

  it("shows an error", async () => {
    stubApi(() => ({ status: 500, body: { code: "internal", message: "Something went wrong.", hint: "Try again." } }));
    await renderWidget(CiFailures);
    expect((await screen.findByRole("alert")).textContent).toContain("Something went wrong.");
  });
});
