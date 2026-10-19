import { createMemoryHistory } from "@tanstack/react-router";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import type { ProjectDTO, RepoCountsDTO, RepoStatusDTO } from "../../api/gen/api.gen";
import { createAppRouter } from "../../router";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";
import { patchRepo, repoKeys } from "./hooks";
import { relativeTime } from "./ui";

const counts: RepoCountsDTO = { all: 2, uncommitted: 1, behind: 1, ahead: 0, failedPipeline: 0, problems: 1 };

const repo = (over: Partial<RepoStatusDTO>): RepoStatusDTO => ({
  id: "r1",
  projectId: "p1",
  name: "shop-api",
  relPath: "shop-api",
  path: "/work/acme-shop/shop-api",
  trusted: false,
  refreshing: false,
  view: "fresh",
  flags: [],
  branch: "main",
  detached: false,
  upstream: "origin/main",
  hasUpstream: true,
  ahead: 0,
  behind: 2,
  staged: 0,
  unstaged: 3,
  untracked: 1,
  conflicted: 0,
  failedPipeline: false,
  lastCommit: { oid: "abc", subject: "Fix cart total", author: "Ada", at: new Date().toISOString() },
  ...over,
});

const project: ProjectDTO = {
  id: "p1",
  name: "acme-shop",
  path: "/work/acme-shop",
  counts,
  repos: [repo({}), repo({ id: "r2", name: "shop-web", flags: ["no_upstream"], behind: 0, unstaged: 0, untracked: 0 })],
};

function renderPage(handler: (c: Call) => Reply | undefined) {
  const api = stubApi(handler);
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/projects"] }));
  FakeSocket.instances = [];
  const socket = new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket });
  render(<App router={router} socket={socket} />);
  return api;
}

const list = (c: Call): Reply | undefined =>
  c.path.startsWith("/api/repos?") ? { body: { projects: [project], counts } } : undefined;

async function clickAdd() {
  const [first] = await screen.findAllByRole("button", { name: "Add project" });
  if (!first) throw new Error("no Add project button");
  fireEvent.click(first);
}

describe("projects page", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
  });

  it("shows repos with branch, counts, last commit and health badges", async () => {
    renderPage(list);
    const section = await screen.findByRole("region", { name: "acme-shop" });
    expect(within(section).getByText("shop-api")).toBeTruthy();
    expect(within(section).getByText("3 modified, 1 untracked")).toBeTruthy();
    expect(within(section).getByText("0 ahead / 2 behind")).toBeTruthy();
    expect(within(section).getAllByText("Fix cart total").length).toBe(2);
    expect(within(section).getByLabelText(/^no upstream:/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Behind (1)" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Builds (0)" })).toBeTruthy();
  });

  it("asks the server for the chosen filter", async () => {
    const { calls } = renderPage(list);
    await screen.findByRole("region", { name: "acme-shop" });
    fireEvent.click(screen.getByRole("button", { name: "Behind (1)" }));
    await waitFor(() => {
      expect(calls.some((c) => c.path === "/api/repos?filter=behind")).toBe(true);
    });
  });

  it("shows an empty state with Add project", async () => {
    renderPage((c) => (c.path.startsWith("/api/repos?") ? { body: { projects: [], counts } } : undefined));
    expect(await screen.findByText(/Nothing is tracked yet/)).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "Add project" }).length).toBeGreaterThan(0);
  });

  it("browses a folder, previews, and tracks the project", async () => {
    const { calls } = renderPage((c) => {
      if (c.path === "/api/fs/dirs?path=%2Fwork%2Facme-shop") {
        return { body: { path: "/work/acme-shop", parent: "/work", entries: [] } };
      }
      if (c.path.startsWith("/api/fs/dirs?path=%2Fwork")) {
        return { body: { path: "/work", parent: "", entries: [{ name: "acme-shop", path: "/work/acme-shop", isRepo: false }] } };
      }
      if (c.path.startsWith("/api/fs/dirs")) {
        return { body: { path: "", parent: "", entries: [{ name: "/work", path: "/work", isRepo: false }] } };
      }
      if (c.path === "/api/projects/preview") {
        return {
          body: {
            path: "/work/acme-shop",
            truncated: true,
            skipped: 0,
            candidates: [
              { relPath: "shop-api", name: "shop-api", active: false },
              { relPath: "shop-web", name: "shop-web", active: true },
            ],
          },
        };
      }
      if (c.path === "/api/projects" && c.method === "POST") return { status: 201, body: { projectId: "p1", added: ["r1"], restored: [], alreadyTracked: false } };
      return list(c);
    });
    await clickAdd();
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(await within(dialog).findByRole("button", { name: "/work" }));
    fireEvent.click(await within(dialog).findByRole("button", { name: "acme-shop" }));
    await within(dialog).findByText("/work/acme-shop");
    fireEvent.click(within(dialog).getByRole("button", { name: "Preview this folder" }));

    expect(await within(dialog).findByText("already tracked")).toBeTruthy();
    expect(within(dialog).getByText(/Showing the first 2/)).toBeTruthy();
    expect((within(dialog).getByLabelText<HTMLInputElement>("Project name")).value).toBe("acme-shop");
    fireEvent.click(within(dialog).getByRole("button", { name: "Track" }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    const post = calls.find((c) => c.path === "/api/projects" && c.method === "POST");
    expect(post?.body).toEqual({ path: "/work/acme-shop", name: "acme-shop", picks: [{ relPath: "shop-api", name: "shop-api" }] });
  });

  it("shows the API's plain-language error when tracking fails", async () => {
    renderPage((c) => {
      if (c.path.startsWith("/api/fs/dirs")) return { body: { path: "/work", parent: "", entries: [] } };
      if (c.path === "/api/projects/preview") return { body: { path: "/work", truncated: false, skipped: 0, candidates: [] } };
      if (c.path === "/api/projects" && c.method === "POST") {
        return { status: 409, body: { code: "conflict", message: 'A project called "work" is already tracked.', hint: "Pick another name." } };
      }
      return list(c);
    });
    await clickAdd();
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(await within(dialog).findByRole("button", { name: "Preview this folder" }));
    fireEvent.click(await within(dialog).findByRole("button", { name: "Track" }));
    expect((await within(dialog).findByRole("alert")).textContent).toContain("already tracked");
  });

  it("closes the add dialog with Escape", async () => {
    renderPage(list);
    await clickAdd();
    const dialog = await screen.findByRole("dialog");
    fireEvent.keyDown(dialog, { key: "Escape" });
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
  });

  it("confirms before stopping tracking a repo and shows a busy conflict as is", async () => {
    const { calls } = renderPage((c) => {
      if (c.path === "/api/repos/r1/untrack") {
        return { status: 409, body: { code: "conflict", message: "An agent task is queued or running in this repo, so it can't be untracked now." } };
      }
      return list(c);
    });
    const menu = await screen.findByRole("button", { name: "Actions for shop-api" });
    fireEvent.keyDown(menu, { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: "Stop tracking repo" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("Nothing on disk is touched");
    expect(calls.some((c) => c.path === "/api/repos/r1/untrack")).toBe(false);
    fireEvent.click(within(dialog).getByRole("button", { name: "Stop tracking" }));
    expect((await within(dialog).findByRole("alert")).textContent).toContain("queued or running");
  });

  it("confirms before stopping tracking a project", async () => {
    const { calls } = renderPage((c) => (c.path === "/api/projects/p1/untrack" ? { body: { repos: ["r1", "r2"] } } : list(c)));
    const menu = await screen.findByRole("button", { name: "Actions for project acme-shop" });
    fireEvent.keyDown(menu, { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: "Stop tracking project" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("Nothing on disk is touched");
    fireEvent.click(within(dialog).getByRole("button", { name: "Stop tracking project" }));
    await waitFor(() => {
      expect(calls.some((c) => c.path === "/api/projects/p1/untrack" && c.method === "POST")).toBe(true);
    });
  });

  it("applies a repo:<id> message to the visible row and refetches on repos", async () => {
    const { calls } = renderPage(list);
    await screen.findByText("0 ahead / 2 behind");
    const socket = FakeSocket.last();
    act(() => {
      socket.open();
    });
    expect(socket.sent).toContainEqual({ type: "subscribe", topic: "repo:r1" });
    act(() => {
      socket.receive({ type: "event", topic: "repo:r1", seq: 1, data: repo({ ahead: 4, behind: 0 }) });
    });
    expect(await screen.findByText("4 ahead / 0 behind")).toBeTruthy();

    const before = calls.filter((c) => c.path.startsWith("/api/repos?")).length;
    act(() => {
      socket.receive({ type: "event", topic: "repos", seq: 1, data: { counts } });
    });
    await waitFor(() => {
      expect(calls.filter((c) => c.path.startsWith("/api/repos?")).length).toBeGreaterThan(before);
    });
  });

  it("refreshes on window focus, at most once per 15 s", async () => {
    const { calls } = renderPage(list);
    await screen.findByRole("region", { name: "acme-shop" });
    fireEvent.focus(window);
    fireEvent.focus(window);
    await waitFor(() => {
      expect(calls.filter((c) => c.path === "/api/repos/refresh")).toHaveLength(1);
    });
    expect(calls.find((c) => c.path === "/api/repos/refresh")?.body).toEqual({ ids: [], reason: "focus" });
  });
});

describe("helpers", () => {
  it("patchRepo replaces only the matching row", () => {
    const data = { projects: [project], counts };
    const out = patchRepo(data, repo({ ahead: 9 }));
    expect(out?.projects[0]?.repos[0]?.ahead).toBe(9);
    expect(out?.projects[0]?.repos[1]?.name).toBe("shop-web");
    expect(repoKeys.list("all")).toEqual(["repos", "list", "all"]);
  });

  it("relativeTime", () => {
    const now = Date.parse("2026-01-10T12:00:00Z");
    expect(relativeTime("2026-01-10T11:00:00Z", now)).toBe("1 hour ago");
    expect(relativeTime("2026-01-08T12:00:00Z", now)).toBe("2 days ago");
  });
});
