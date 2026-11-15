import { createMemoryHistory } from "@tanstack/react-router";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import { createAppRouter } from "../../router";
import { useLayoutStore } from "../../stores/layout";
import { stubApi } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";
import { widgetRegistry } from "../../widgets/registry";
import { LAYOUT_KEY, defaultLayout } from "./layout";

async function renderHome() {
  const usage = { inputTokens: 0, outputTokens: 0, cacheWriteTokens: 0, cacheReadTokens: 0, costMicroUsd: 0, costKnown: false, durationMs: 0 };
  stubApi((c) => {
    if (c.path === "/api/board") {
      const card = { taskId: "tsk_1", title: "Find the order routes", mode: "ask", agent: "demo", status: "running", column: "working", repoNames: [], pendingProposals: 0, usage, updatedAt: "2026-01-01T00:00:00Z" };
      return { body: { maxAgents: 2, active: 1, hint: { raiseTo: 0 }, cards: [card] } };
    }
    return c.path.startsWith("/api/tasks?")
      ? { body: { tasks: [{ id: "tsk_1", title: "Find the order routes", status: "running" }] } }
      : undefined;
  });
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
  // Finish the initial router/query lifecycle before querying the dashboard.
  await act(() => {
    render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
    return Promise.resolve();
  });
  // Keep accessible control queries inside their owning landmark. Global
  // absence checks and portaled menu queries still cover the whole page.
  return within(screen.getByRole("region", { name: "Widgets" }));
}

function saved(): { version: number; items: { id: string }[] } {
  return JSON.parse(localStorage.getItem(LAYOUT_KEY) ?? "null") as { version: number; items: { id: string }[] };
}

describe("widget grid", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    useLayoutStore.setState({ items: defaultLayout(widgetRegistry), customizing: false });
  });

  it("shows the registered widgets with live data", async () => {
    await renderHome();
    expect(await screen.findByRole("heading", { name: "Working now" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "This machine" })).toBeTruthy();
    const link = (await screen.findAllByRole("link", { name: "Find the order routes" }))[0] as HTMLElement;
    expect(link.getAttribute("href")).toBe("/tasks/tsk_1");
    await waitFor(() => {
      expect(screen.getByText("test")).toBeTruthy(); // the server version, from /api/health
    });
    expect((await screen.findByRole("status")).textContent).toContain("1 running");
  });

  it("shows Remove, Add and Reset only in Customize mode", async () => {
    const widgets = await renderHome();
    const toggle = await widgets.findByRole("button", { name: "Customize" });
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reset layout" })).toBeNull();

    fireEvent.click(toggle);
    expect(widgets.getByRole("button", { name: "Done" }).getAttribute("aria-pressed")).toBe("true");
    expect(widgets.getByRole("button", { name: "Remove Working now" })).toBeTruthy();
    expect(widgets.getByRole("button", { name: "Reset layout" })).toBeTruthy();
    expect(widgets.getByRole<HTMLButtonElement>("button", { name: "Add widget" }).disabled).toBe(true); // all are placed

    fireEvent.click(widgets.getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
  });

  it("removes a widget, remembers that, and brings it back with Reset layout", async () => {
    const widgets = await renderHome();
    fireEvent.click(await widgets.findByRole("button", { name: "Customize" }));
    fireEvent.click(widgets.getByRole("button", { name: "Remove Working now" }));

    expect(screen.queryByRole("heading", { name: "Working now" })).toBeNull();
    expect(saved().version).toBe(1);
    expect(saved().items.map((i) => i.id)).toEqual(widgetRegistry.map((w) => w.id).filter((id) => id !== "working-now"));
    expect(widgets.getByRole<HTMLButtonElement>("button", { name: "Add widget" }).disabled).toBe(false);

    fireEvent.click(widgets.getByRole("button", { name: "Reset layout" }));
    expect(await screen.findByRole("heading", { name: "Working now" })).toBeTruthy();
    expect(saved().items.map((i) => i.id)).toEqual(widgetRegistry.map((w) => w.id));
  });

  it("adds a removed widget back from the menu", async () => {
    const widgets = await renderHome();
    fireEvent.click(await widgets.findByRole("button", { name: "Customize" }));
    fireEvent.click(widgets.getByRole("button", { name: "Remove This machine" }));
    expect(screen.queryByRole("heading", { name: "This machine" })).toBeNull();

    fireEvent.keyDown(widgets.getByRole("button", { name: "Add widget" }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: "This machine" }));
    expect(await screen.findByRole("heading", { name: "This machine" })).toBeTruthy();
    expect(saved().items.map((i) => i.id).sort()).toEqual(widgetRegistry.map((w) => w.id).sort());
  });

  it("explains an empty grid", async () => {
    useLayoutStore.setState({ items: [] });
    await renderHome();
    expect(await screen.findByText(/No widgets here/)).toBeTruthy();
  });
});
