import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import { createAppRouter } from "../../router";
import { stubApi, type Handler } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";

const task = (status: string) => ({
  id: "t1",
  projectId: "p1",
  title: "Find the order routes",
  prompt: "Find the order routes.",
  scope: "repo",
  repoIds: ["r1"],
  mode: "ask",
  agent: "mock",
  status,
  createdAt: "2026-10-16T10:00:00Z",
  updatedAt: "2026-10-16T10:00:00Z",
});

const events = [
  { seq: 1, kind: "message", at: "2026-10-16T10:00:01Z", role: "assistant", text: "Looking at the routes." },
  { seq: 2, kind: "subagent_start", at: "2026-10-16T10:00:02Z", id: "sa", text: "Read the router" },
  { seq: 3, kind: "tool_call", at: "2026-10-16T10:00:03Z", parentId: "sa", tool: { name: "Read" } },
  { seq: 4, kind: "usage", at: "2026-10-16T10:00:04Z", usage: { inputTokens: 800, outputTokens: 200, costUsd: 0.01, turns: 2 } },
];

function renderTask(status: string, extra?: Handler) {
  const api = stubApi((c) => {
    const custom = extra?.(c);
    if (custom) return custom;
    if (c.path === "/api/tasks/t1") return { body: { task: task(status) } };
    if (c.path.startsWith("/api/tasks/t1/events")) return { body: { events } };
    return undefined;
  });
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/tasks/t1"] }));
  const socket = new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket, baseDelayMs: 1 });
  render(<App router={router} socket={socket} />);
  return api;
}

describe("task page", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
  });

  it("shows the title, status, totals and the transcript with its sub-agent tree", async () => {
    renderTask("running");
    expect((await screen.findByRole("heading", { name: "Find the order routes" })).textContent).toBe("Find the order routes");
    expect((await screen.findByTestId("task-status")).textContent).toBe("Running");
    expect(await screen.findByText("Looking at the routes.")).toBeTruthy();
    expect(screen.getByText("Sub-agent: Read the router")).toBeTruthy();
    expect(screen.getByText("1.0k tokens · $0.0100 · 2 turns")).toBeTruthy();
  });

  it("offers Cancel while the task runs and posts it", async () => {
    const api = renderTask("running");
    fireEvent.click(await screen.findByRole("button", { name: "Cancel task" }));
    await waitFor(() => {
      expect(api.calls).toContainEqual(expect.objectContaining({ method: "POST", path: "/api/tasks/t1/cancel" }));
    });
  });

  it("offers no Cancel for a finished task", async () => {
    renderTask("done");
    expect((await screen.findByTestId("task-status")).textContent).toBe("Done");
    expect(screen.queryByRole("button", { name: "Cancel task" })).toBeNull();
  });

  it("explains in plain words when the task doesn't exist", async () => {
    stubApi(); // the default answers 404 for a task
    const router = createAppRouter(createMemoryHistory({ initialEntries: ["/tasks/nope"] }));
    render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
    expect((await screen.findByRole("alert")).textContent).toContain("doesn't exist");
    expect(screen.getByRole("heading", { name: "Task" })).toBeTruthy();
  });
});
