import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import type { RepoHostResponse, RepoStatusDTO } from "../../api/gen/api.gen";
import { createAppRouter } from "../../router";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";
import { parseDiff } from "./parseDiff";

const counts = { all: 1, uncommitted: 1, behind: 0, ahead: 1, failedPipeline: 0, problems: 0 };
const repo: RepoStatusDTO = {
  id: "r1", projectId: "p1", name: "shop-api", relPath: "shop-api", path: "/work/acme-shop/shop-api",
  trusted: false, refreshing: false, view: "fresh", flags: [], branch: "feature/cart", detached: false,
  upstream: "origin/feature/cart", hasUpstream: true, ahead: 2, behind: 1, staged: 0, unstaged: 1, untracked: 0,
  conflicted: 0, failedPipeline: false,
  lastCommit: { oid: "abc", subject: "Fix cart total", author: "Ada", at: new Date().toISOString() },
};
const repos = { projects: [{ id: "p1", name: "acme-shop", path: "/work/acme-shop", counts, repos: [repo] }], counts };

const host: RepoHostResponse = {
  linked: true, project: "acme-shop/shop-api", branch: "feature/cart", phase: "idle", credentialsInRemote: true,
  mr: { number: 12, title: "Cart totals", url: "https://git.acme.dev/mr/12", author: "ada", source: "a", target: "main", state: "open", updatedAt: new Date().toISOString() },
  pipeline: { status: "failed", url: "https://git.acme.dev/pipe/1", updatedAt: new Date().toISOString() },
  links: { repo: "https://git.acme.dev/r", branch: "https://git.acme.dev/b", mrList: "https://git.acme.dev/m", pipelines: "https://git.acme.dev/p" },
};

const diffText = "diff --git a/cart.ts b/cart.ts\n--- a/cart.ts\n+++ b/cart.ts\n@@ -1,2 +1,2 @@\n ctx\n-old\n+new\n";
const commit = { oid: "c1", short: "c1", subject: "Add cart", author: "Ada", email: "a@acme.dev", at: new Date().toISOString(), parents: [] };

function handler(c: Call): Reply | undefined {
  const p = c.path;
  if (p.startsWith("/api/repos?")) return { body: repos };
  if (p === "/api/repos/r1/host") return { body: host };
  if (p === "/api/repos/r1/changes")
    return { body: { truncated: false, files: [{ path: "cart.ts", index: " ", worktree: "M" }, { path: "staged.ts", index: "A", worktree: " " }] } };
  if (p.startsWith("/api/repos/r1/diff?")) return { body: { text: diffText, truncated: p.includes("staged.ts") && p.includes("mode=staged"), binary: false } };
  if (p.startsWith("/api/repos/r1/log")) return { body: { commits: [commit] } };
  if (p === "/api/repos/r1/commits/c1")
    return { body: { commit, body: "Longer body", files: [{ path: "cart.ts", status: "A" }], diff: { text: diffText, truncated: false, binary: false } } };
  if (p === "/api/repos/r1/branches")
    return { body: { branches: [{ name: "main", remote: false, current: false, oid: "1234567890" }, { name: "feature/cart", remote: false, current: true, upstream: "origin/feature/cart", oid: "abcdef0123" }, { name: "origin/main", remote: true, current: false, oid: "1234567890" }] } };
  return undefined;
}

function renderAt(path: string, h: (c: Call) => Reply | undefined = handler) {
  const api = stubApi(h);
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
  FakeSocket.instances = [];
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
  return api;
}

describe("repo page", () => {
  it("renders header and host block", async () => {
    renderAt("/repos/r1");
    expect(await screen.findByRole("heading", { name: "shop-api" })).toBeTruthy();
    expect(screen.getByText(/2 ahead \/ 1 behind/)).toBeTruthy();
    expect(await screen.findByRole("link", { name: /#12 Cart totals/ })).toHaveProperty("href", "https://git.acme.dev/mr/12");
    expect(screen.getByRole("link", { name: "Open pipeline" })).toBeTruthy();
    expect(screen.getByText(/remote URL contains credentials/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Pipelines" })).toBeTruthy();
  });

  it("renders no anchor for javascript: MR and pipeline URLs", async () => {
    const evil: RepoHostResponse = {
      ...host,
      mr: { ...(host.mr as NonNullable<typeof host.mr>), url: "javascript:alert(1)" },
      pipeline: { ...(host.pipeline as NonNullable<typeof host.pipeline>), url: " JaVaScRiPt:alert(2)" },
    };
    renderAt("/repos/r1", (c) => (c.path === "/api/repos/r1/host" ? { body: evil } : handler(c)));
    expect(await screen.findByText(/#12 Cart totals/)).toBeTruthy();
    expect(screen.queryByRole("link", { name: /#12 Cart totals/ })).toBeNull();
    expect(screen.queryByRole("link", { name: "Open pipeline" })).toBeNull();
    expect(screen.getByText("Open pipeline")).toBeTruthy();
    expect(document.querySelector('a[href*="javascript" i]')).toBeNull();
  });

  it("shows a blocked phase reason", async () => {
    renderAt("/repos/r1", (c) => (c.path === "/api/repos/r1/host" ? { body: { ...host, phase: "blocked", blockReason: "unauthorized", mr: undefined, pipeline: undefined } } : handler(c)));
    expect(await screen.findByText(/Add a token/)).toBeTruthy();
  });

  it("changes: selects a file, shows diff, toggles staged", async () => {
    const api = renderAt("/repos/r1");
    fireEvent.click(await screen.findByRole("button", { name: /cart\.ts/ }));
    expect(await screen.findByRole("region", { name: "Diff of cart.ts" })).toBeTruthy();
    expect(api.calls.some((c) => c.path === "/api/repos/r1/diff?path=cart.ts&mode=unstaged")).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Staged" }));
    await waitFor(() => { expect(api.calls.some((c) => c.path === "/api/repos/r1/diff?path=cart.ts&mode=staged")).toBe(true); });
    fireEvent.click(screen.getByRole("button", { name: /staged\.ts/ }));
    expect(await screen.findByText(/too large/)).toBeTruthy();
  });

  it("history: shows commit detail", async () => {
    renderAt("/repos/r1");
    fireEvent.click(await screen.findByRole("tab", { name: "History" }));
    fireEvent.click(await screen.findByRole("button", { name: /Add cart/ }));
    expect(await screen.findByText("Longer body")).toBeTruthy();
    expect(screen.getByRole("region", { name: "Diff of c1" })).toBeTruthy();
  });

  it("branches: marks the current one", async () => {
    renderAt("/repos/r1");
    fireEvent.click(await screen.findByRole("tab", { name: "Branches" }));
    const local = await screen.findByRole("region", { name: "Local branches" });
    expect(within(local).getByText("current")).toBeTruthy();
    expect(screen.getByRole("region", { name: "Remote branches" })).toBeTruthy();
  });

  it("untracked repo shows a way back", async () => {
    renderAt("/repos/nope");
    expect(await screen.findByText("This repo isn't tracked")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Back to projects" })).toBeTruthy();
  });

  it("tabs and file list work with the keyboard", async () => {
    renderAt("/repos/r1");
    const changes = await screen.findByRole("tab", { name: "Changes" });
    changes.focus();
    fireEvent.keyDown(changes, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: "History" }).getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(screen.getByRole("tab", { name: "History" }), { key: "ArrowLeft" });
    const first = await screen.findByRole("button", { name: /cart\.ts/ });
    first.focus();
    fireEvent.keyDown(first, { key: "ArrowDown" });
    expect(document.activeElement).toBe(screen.getByRole("button", { name: /staged\.ts/ }));
  });
});

describe("parseDiff", () => {
  it("numbers lines", () => {
    const l = parseDiff(diffText).filter((x) => x.kind !== "meta");
    expect(l.map((x) => [x.kind, x.oldNo, x.newNo])).toEqual([["hunk", undefined, undefined], ["ctx", 1, 1], ["del", 2, undefined], ["add", undefined, 2]]);
  });
});
