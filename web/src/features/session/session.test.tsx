import { createMemoryHistory } from "@tanstack/react-router";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import { createAppRouter } from "../../router";
import { stubApi } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";
import { isSessionRequired, useSessionStore } from "./store";

afterEach(() => {
  useSessionStore.setState({ state: { tag: "ok" } });
  FakeSocket.instances = [];
});

describe("isSessionRequired", () => {
  it.each([
    [401, "session_required", true],
    [401, "unauthorized", false],
    [403, "session_required", false],
    [200, "session_required", false],
  ])("%i %s", (status, code, want) => {
    expect(isSessionRequired(status, code)).toBe(want);
  });
});

describe("session required page", () => {
  const mount = () => {
    const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
    const socket = new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket, baseDelayMs: 1 });
    render(<App router={router} socket={socket} />);
  };

  it("replaces the app when an API call answers 401 session_required", async () => {
    stubApi(() => ({ status: 401, body: { code: "session_required", message: "Open Yggdrasil from the link." } }));
    mount();
    expect(await screen.findByText("Open Yggdrasil from the link in the terminal")).toBeTruthy();
    expect(document.body.textContent).toContain("?k=");
  });

  it("probes the API when the socket never opens, and shows the page on 401", async () => {
    const api = stubApi((c) =>
      c.path === "/api/agents" ? { status: 401, body: { code: "session_required", message: "x" } } : { body: {} },
    );
    mount();
    FakeSocket.last().onclose?.();
    await screen.findByText("Open Yggdrasil from the link in the terminal");
    expect(api.calls.filter((c) => c.path === "/api/agents").length).toBeGreaterThanOrEqual(1);
  });
});
