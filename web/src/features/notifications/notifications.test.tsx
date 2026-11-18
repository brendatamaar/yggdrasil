import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NoticeDTO } from "../../api/gen/api.gen";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { initialDelivery } from "./core";
import { NoticeMenu } from "./NoticeMenu";
import { NoticeSync } from "./NoticeSync";
import { useDeliveryStore, useNoticeReadStore } from "./store";

function App() {
  return (
    <>
      <NoticeSync />
      <NoticeMenu />
    </>
  );
}

const n = (cursor: number, over: Partial<NoticeDTO> = {}): NoticeDTO => ({
  id: `n${String(cursor)}`,
  taskId: "t1",
  taskTitle: `Task ${String(cursor)}`,
  status: "needs_you",
  revision: cursor,
  createdAt: "2026-01-01T10:00:00Z",
  cursor,
  ...over,
});

/** A server whose history can change between reads. */
function server() {
  const s = { notices: [n(2), n(1)], gap: false, fail: false };
  const handler = (c: Call): Reply | undefined => {
    if (!c.path.startsWith("/api/notifications")) return undefined;
    if (s.fail) return { status: 500, body: { code: "internal", message: "The server hit a problem.", hint: "Try again." } };
    const after = new URL(c.path, "http://x").searchParams.get("after");
    const list = after === null ? s.notices : s.notices.filter((x) => x.cursor > Number(after)).sort((a, b) => a.cursor - b.cursor);
    const cursors = s.notices.map((x) => x.cursor);
    return { body: { notices: list, oldest: Math.min(...cursors, 0), newest: Math.max(0, ...cursors), gap: s.gap && after !== null } };
  };
  return Object.assign(handler, { s });
}

let shown: { title: string; options?: NotificationOptions }[] = [];
let requested = 0;
let throwOnShow = false;

function fakeNotification(permission: NotificationPermission) {
  class FakeNotification {
    static permission = permission;
    static requestPermission = () => {
      requested++;
      FakeNotification.permission = "granted";
      return Promise.resolve("granted" as NotificationPermission);
    };
    readonly title: string;
    constructor(title: string, options?: NotificationOptions) {
      if (throwOnShow) throw new Error("blocked");
      this.title = title;
      shown.push(options ? { title, options } : { title });
    }
  }
  vi.stubGlobal("Notification", FakeNotification);
}

function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { value: state, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  shown = [];
  requested = 0;
  throwOnShow = false;
  useDeliveryStore.setState({ state: initialDelivery() });
  useNoticeReadStore.setState({ readIds: [] });
  setVisibility("visible");
});
afterEach(() => {
  setVisibility("visible");
});

const live = (r: Awaited<ReturnType<typeof renderWidget>>, cursor: number) => {
  r.push("notices", { noticeId: `n${String(cursor)}`, cursor });
};

describe("notice list", () => {
  it("shows task name and status only, with an unread count that clears when opened", async () => {
    stubApi(server());
    await renderWidget(App);
    const btn = await screen.findByRole("button", { name: "Notifications, 2 unread" });
    fireEvent.click(btn);
    const list = await screen.findByRole("list", { name: "Notifications" });
    expect(list.textContent).toBe("Task 2: Needs youTask 1: Needs you");
    expect(await screen.findByRole("button", { name: "Notifications" })).toBeTruthy();
    expect(useNoticeReadStore.getState().readIds.sort()).toEqual(["n1", "n2"]);
  });

  it("an empty history is its own state, not an error", async () => {
    const h = server();
    h.s.notices = [];
    stubApi(h);
    await renderWidget(App);
    fireEvent.click(await screen.findByRole("button", { name: "Notifications" }));
    const empty = await screen.findByText(/No notifications yet/);
    expect(empty.getAttribute("data-empty")).toBe("NoNotifications");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("a failed load is an error with a retry, never the empty message", async () => {
    const h = server();
    h.s.fail = true;
    stubApi(h);
    await renderWidget(App);
    fireEvent.click(await screen.findByRole("button", { name: "Notifications" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Couldn't load notifications");
    expect(screen.queryByText(/No notifications yet/)).toBeNull();
    h.s.fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list", { name: "Notifications" })).toBeTruthy();
  });

  it("rejects a payload with an unknown status instead of showing it", async () => {
    stubApi((c) =>
      c.path.startsWith("/api/notifications")
        ? { body: { notices: [{ ...n(1), status: "<script>" }], oldest: 1, newest: 1, gap: false } }
        : undefined,
    );
    await renderWidget(App);
    fireEvent.click(await screen.findByRole("button", { name: "Notifications" }));
    expect((await screen.findByRole("alert")).textContent).toContain("does not understand");
  });
});

describe("browser delivery in the app", () => {
  it("never asks for permission on its own; only a click on the button does", async () => {
    fakeNotification("default");
    stubApi(server());
    await renderWidget(App);
    fireEvent.click(await screen.findByRole("button", { name: /Notifications/ }));
    const turnOn = await screen.findByRole("button", { name: "Turn on desktop notices" });
    expect(requested).toBe(0);
    fireEvent.click(turnOn);
    await waitFor(() => {
      expect(requested).toBe(1);
    });
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Turn on desktop notices" })).toBeNull();
    });
  });

  it("a live notice in a background tab shows one browser notice with the task name and status, once", async () => {
    fakeNotification("granted");
    const h = server();
    stubApi(h);
    const r = await renderWidget(App);
    await screen.findByRole("button", { name: /Notifications/ });
    await waitFor(() => {
      expect(useDeliveryStore.getState().state.tag).toBe("granted");
    });
    await waitFor(() => {
      expect(useDeliveryStore.getState().state.delivered.has("n2")).toBe(true);
    });
    setVisibility("hidden");

    h.s.notices = [n(3, { taskTitle: "Add cart page", status: "ready_for_review" }), ...h.s.notices];
    live(r, 3);
    await waitFor(() => {
      expect(shown).toHaveLength(1);
    });
    expect(shown[0]).toEqual({ title: "Add cart page", options: { body: "Ready for review", tag: "n3" } });

    live(r, 3); // the same event again: the history read finds nothing new
    await new Promise((res) => setTimeout(res, 30));
    expect(shown).toHaveLength(1);
  });

  it("a gap reload fills the list but never shows a browser notice", async () => {
    fakeNotification("granted");
    const h = server();
    stubApi(h);
    const r = await renderWidget(App);
    await waitFor(() => {
      expect(useDeliveryStore.getState().state.tag).toBe("granted");
    });
    await waitFor(() => {
      expect(useDeliveryStore.getState().state.delivered.has("n1")).toBe(true);
    });
    setVisibility("hidden");
    h.s.gap = true;
    h.s.notices = [n(9), n(8), n(7)];
    live(r, 9);
    fireEvent.click(await screen.findByRole("button", { name: /Notifications/ }));
    await screen.findByText("Task 9");
    expect(shown).toHaveLength(0);
  });

  it("when the browser API throws, the notice is still in the app list", async () => {
    fakeNotification("granted");
    throwOnShow = true;
    const h = server();
    stubApi(h);
    const r = await renderWidget(App);
    await waitFor(() => {
      expect(useDeliveryStore.getState().state.delivered.has("n2")).toBe(true);
    });
    setVisibility("hidden");
    h.s.notices = [n(3, { taskTitle: "Fix checkout" }), ...h.s.notices];
    live(r, 3);
    fireEvent.click(await screen.findByRole("button", { name: /Notifications/ }));
    expect(await screen.findByText("Fix checkout")).toBeTruthy();
    expect(shown).toHaveLength(0);
  });

  it("an unsupported browser still lists notices and says so", async () => {
    vi.stubGlobal("Notification", undefined);
    stubApi(server());
    await renderWidget(App);
    fireEvent.click(await screen.findByRole("button", { name: /Notifications/ }));
    expect(await screen.findByText(/can't show desktop notices/)).toBeTruthy();
    expect(await screen.findByText("Task 2")).toBeTruthy();
  });

  it("denied permission is explained and asks nothing", async () => {
    fakeNotification("denied");
    stubApi(server());
    await renderWidget(App);
    fireEvent.click(await screen.findByRole("button", { name: /Notifications/ }));
    expect(await screen.findByText(/blocked in your browser/)).toBeTruthy();
    expect(requested).toBe(0);
  });
});
