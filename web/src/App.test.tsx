import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "./App";
import { WSClient } from "./api/ws";
import { createAppRouter } from "./router";
import { useCommandStore } from "./stores/commands";
import { useNotificationStore } from "./stores/notifications";
import { FakeSocket } from "./test/fakeSocket";

function renderAt(path: string) {
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
  const socket = new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket });
  render(<App router={router} socket={socket} />);
}

describe("app shell", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    useNotificationStore.setState({ items: [] });
    useCommandStore.setState({ open: false });
  });

  it("shows the top bar with theme toggle, status pill and notifications", async () => {
    renderAt("/");

    const banner = await screen.findByRole("banner");
    expect(within(banner).getByText("Yggdrasil")).toBeTruthy();
    expect(within(banner).getByRole("button", { name: /change theme/i })).toBeTruthy();
    expect(within(banner).getByRole("status").textContent).toContain("0 running");
    expect(within(banner).getByRole("button", { name: "Notifications" })).toBeTruthy();
  });

  it("status pill follows the connection", async () => {
    renderAt("/");
    const pill = (await screen.findByRole("status")).textContent;
    expect(pill).toContain("Connecting");

    FakeSocket.last().open();
    expect((await screen.findByRole("status")).textContent).toContain("Connected");
  });

  it.each([
    ["/projects", "Projects"],
    ["/settings", "Settings"],
    ["/tasks/t-1", "Task"],
  ])("renders %s", async (path, title) => {
    renderAt(path);
    expect(await screen.findByRole("heading", { name: title })).toBeTruthy();
  });

  it("renders the not-found page for unknown paths", async () => {
    renderAt("/some/deep/link");

    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeTruthy();
    expect(screen.getByRole("banner")).toBeTruthy();
  });

  it("opens the palette with Ctrl+K and runs a command", async () => {
    renderAt("/");
    await screen.findByRole("banner");

    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    fireEvent.click(await screen.findByText("Go to Settings"));

    expect(await screen.findByRole("heading", { name: "Settings" })).toBeTruthy();
  });

  it("lists notifications with task name and status only", async () => {
    useNotificationStore.getState().notify("Add cart page", "needs_you");
    renderAt("/");

    fireEvent.click(await screen.findByRole("button", { name: /notifications, 1 unread/i }));
    const item = await screen.findByText("Add cart page");
    expect(item.parentElement?.textContent).toBe("Add cart page: Needs you");
  });
});
