import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ReposResponse } from "../../api/gen/api.gen";
import { renderWidget } from "../../test/renderWidget";
import { stubApi } from "../../test/fakeApi";
import { RecentAreas } from "./RecentAreas";

const counts = { all: 2, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 0, problems: 0 };
const repo = (id: string, name: string, at?: string) => ({
  id,
  projectId: "p1",
  name,
  relPath: name,
  path: `/work/acme-shop/${name}`,
  trusted: false,
  refreshing: false,
  view: "fresh" as const,
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
  ...(at ? { lastCommit: { oid: "a", subject: "x", author: "Ada", at } } : {}),
});
const reply = (repos: ReturnType<typeof repo>[]): ReposResponse => ({
  counts,
  projects: [{ id: "p1", name: "acme-shop", path: "/work/acme-shop", counts, repos }],
});

describe("Recent areas widget", () => {
  it("shows a loading state first", async () => {
    stubApi(() => ({ body: reply([]) }));
    await renderWidget(RecentAreas);
    expect(screen.getByText("Loading…")).toBeTruthy();
  });

  it("lists recently touched repos with their project", async () => {
    stubApi(() => ({
      body: reply([repo("r1", "shop-api", "2026-01-01T10:00:00Z"), repo("r2", "shop-web", "2026-02-01T10:00:00Z")]),
    }));
    await renderWidget(RecentAreas);
    const links = await screen.findAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(["shop-web", "shop-api"]);
    expect(links[1]?.getAttribute("href")).toBe("/repos/r1");
    expect(screen.getAllByText("acme-shop")).toHaveLength(2);
  });

  it("explains an empty list", async () => {
    stubApi(() => ({ body: reply([]) }));
    await renderWidget(RecentAreas);
    expect(await screen.findByText(/No recent work yet/)).toBeTruthy();
  });

  it("shows an error", async () => {
    stubApi(() => ({ status: 500, body: { code: "internal", message: "Something went wrong.", hint: "Try again." } }));
    await renderWidget(RecentAreas);
    expect((await screen.findByRole("alert")).textContent).toContain("Something went wrong.");
  });
});
