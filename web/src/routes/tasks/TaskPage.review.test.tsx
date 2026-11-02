import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import { createAppRouter } from "../../router";
import { stubApi } from "../../test/fakeApi";
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

function renderTask(status: string, reason?: string) {
  stubApi((c) => {
    if (c.path === "/api/tasks/t1") return { body: { task: task(status) } };
    if (c.path === "/api/proposals?task=t1") return { body: { proposals: [] } };
    if (c.path === "/api/repos/r1/trust") return { body: { repoId: "r1", state: "none", files: [] } };
    if (c.path === "/api/board") {
      const cards = [{ taskId: "t1", title: "x", status, column: "needs_you", ...(reason ? { needsYouReason: reason } : {}) }];
      return { body: { maxAgents: 2, active: 0, hint: { raiseTo: 0 }, cards } };
    }
    return undefined;
  });
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/tasks/t1"] }));
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket, baseDelayMs: 1 })} />);
}

describe("task page: Review tab", () => {
  it("opens the Review tab with the arrow key and shows calm 'not available yet' states", async () => {
    renderTask("review");
    const transcript = await screen.findByRole("tab", { name: "Transcript" });
    fireEvent.keyDown(transcript, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: "Review" }).getAttribute("aria-selected")).toBe("true");
    expect(await screen.findByRole("button", { name: "Verify again" })).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: "Next message" })).toBeNull();
  });
});

describe("task page: next message and Needs you", () => {
  it("mounts the message composer when the task is waiting for the next message", async () => {
    renderTask("review");
    expect(await screen.findByRole("textbox", { name: "Next message" })).toBeTruthy();
  });

  it("has no message composer while the task runs", async () => {
    renderTask("running");
    await screen.findByText("Find the order routes");
    expect(screen.queryByRole("textbox", { name: "Next message" })).toBeNull();
  });

  it("explains an Ask-integrity failure from needsYouReason", async () => {
    renderTask("needs_you", "integrity_violated");
    expect(await screen.findByText("Ask mode changed your files")).toBeTruthy();
  });
});
