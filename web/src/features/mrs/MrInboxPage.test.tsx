import { createMemoryHistory } from "@tanstack/react-router";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import type { InboxResponse } from "../../api/gen/api.gen";
import { createAppRouter } from "../../router";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";

const mr = (n: number, title: string) => ({
  number: n,
  title,
  url: `https://git.acme.dev/acme-shop/shop-api/-/merge_requests/${String(n)}`,
  author: "ada",
  source: "fix",
  target: "main",
  state: "open",
  updatedAt: new Date().toISOString(),
});

const inbox: InboxResponse = {
  items: [
    { mr: mr(1, "Fix cart total"), instance: "git.acme.dev", repo: "acme-shop/shop-api", role: "review_requested" },
    { mr: mr(2, "Add search"), instance: "git.acme.dev", repo: "acme-shop/shop-web", role: "authored" },
  ],
  instances: [{ instance: "git.acme.dev", phase: "blocked", blockReason: "unauthorized" }],
};

function renderMrs(handler: (c: Call) => Reply | undefined) {
  const api = stubApi(handler);
  FakeSocket.instances = [];
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/mrs"] }));
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
  return api;
}

describe("MR inbox", () => {
  it("groups by role, shows the blocked reason, refreshes and refetches on hosts", async () => {
    let data = inbox;
    const { calls } = renderMrs((c) => {
      if (c.path === "/api/mrs") return { body: data };
      if (c.path === "/api/hosts/refresh") return { status: 204, body: null };
      return undefined;
    });
    const review = await screen.findByRole("region", { name: "Review requested" });
    expect(within(review).getByRole("link", { name: "Fix cart total" }).getAttribute("href")).toContain("merge_requests/1");
    expect(within(screen.getByRole("region", { name: "Authored by you" })).getByText("Add search")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Assigned to you" })).toBeNull();
    expect(screen.getByText(/the token was refused\. Check \.env/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => {
      expect(calls.some((c) => c.method === "POST" && c.path === "/api/hosts/refresh")).toBe(true);
    });

    data = { ...inbox, items: inbox.items.slice(1) };
    act(() => {
      FakeSocket.last().open();
      FakeSocket.last().receive({ type: "event", topic: "hosts", seq: 1, data: {} });
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: "Review requested" })).toBeNull();
    });
  });

  it("renders no anchor for a javascript: MR URL", async () => {
    const bad = { ...(inbox.items[0] as NonNullable<(typeof inbox.items)[number]>), mr: { ...mr(9, "Evil MR"), url: "javascript:alert(1)" } };
    renderMrs((c) => (c.path === "/api/mrs" ? { body: { ...inbox, items: [bad] } } : undefined));
    expect(await screen.findByText("Evil MR")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Evil MR" })).toBeNull();
    expect(document.querySelector('a[href*="javascript" i]')).toBeNull();
  });

  it("explains that tokens are optional and where .env lives", async () => {
    renderMrs((c) => {
      if (c.path === "/api/mrs") return { body: { items: [], instances: [] } };
      if (c.path === "/api/hosts") return { body: { instances: [], configProblems: [], envPath: "/home/ada/.ygg/.env" } };
      return undefined;
    });
    expect(await screen.findByText(/Tokens are optional/)).toBeTruthy();
    expect(await screen.findByText("/home/ada/.ygg/.env")).toBeTruthy();
  });
});
