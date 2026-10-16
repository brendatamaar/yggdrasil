import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import { createAppRouter } from "../../router";
import { useLayoutStore } from "../../stores/layout";
import { stubApi } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";
import { widgetRegistry } from "../../widgets/registry";
import { LAYOUT_KEY, defaultLayout } from "./layout";

function renderHome() {
  stubApi((c) =>
    c.path.startsWith("/api/tasks?")
      ? { body: { tasks: [{ id: "tsk_1", title: "Find the order routes", status: "running" }] } }
      : undefined,
  );
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
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
    renderHome();
    expect(await screen.findByRole("heading", { name: "Working now" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "This machine" })).toBeTruthy();
    const link = await screen.findByRole("link", { name: "Find the order routes" });
    expect(link.getAttribute("href")).toBe("/tasks/tsk_1");
    await waitFor(() => {
      expect(screen.getByText("test")).toBeTruthy(); // the server version, from /api/health
    });
    expect((await screen.findByRole("status")).textContent).toContain("1 running");
  });

  it("shows Remove, Add and Reset only in Customize mode", async () => {
    renderHome();
    const toggle = await screen.findByRole("button", { name: "Customize" });
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reset layout" })).toBeNull();

    fireEvent.click(toggle);
    expect(screen.getByRole("button", { name: "Done" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "Remove Working now" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Reset layout" })).toBeTruthy();
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Add widget" }).disabled).toBe(true); // all are placed

    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
  });

  it("removes a widget, remembers that, and brings it back with Reset layout", async () => {
    renderHome();
    fireEvent.click(await screen.findByRole("button", { name: "Customize" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove Working now" }));

    expect(screen.queryByRole("heading", { name: "Working now" })).toBeNull();
    expect(saved().version).toBe(1);
    expect(saved().items.map((i) => i.id)).toEqual(["this-machine"]);
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Add widget" }).disabled).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Reset layout" }));
    expect(await screen.findByRole("heading", { name: "Working now" })).toBeTruthy();
    expect(saved().items.map((i) => i.id)).toEqual(["working-now", "this-machine"]);
  });

  it("adds a removed widget back from the menu", async () => {
    renderHome();
    fireEvent.click(await screen.findByRole("button", { name: "Customize" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove This machine" }));
    expect(screen.queryByRole("heading", { name: "This machine" })).toBeNull();

    fireEvent.keyDown(screen.getByRole("button", { name: "Add widget" }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: "This machine" }));
    expect(await screen.findByRole("heading", { name: "This machine" })).toBeTruthy();
    expect(saved().items.map((i) => i.id)).toEqual(["working-now", "this-machine"]);
  });

  it("explains an empty grid", async () => {
    useLayoutStore.setState({ items: [] });
    renderHome();
    expect(await screen.findByText(/No widgets here/)).toBeTruthy();
  });
});
