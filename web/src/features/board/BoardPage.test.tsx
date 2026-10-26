import { createMemoryHistory } from "@tanstack/react-router";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import type { BoardCardDTO, BoardResponse } from "../../api/gen/api.gen";
import { createAppRouter } from "../../router";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";

const usage = { inputTokens: 1000, outputTokens: 500, cacheWriteTokens: 0, cacheReadTokens: 0, costMicroUsd: 4400, costKnown: true, durationMs: 125000 };

export const card = (id: string, title: string, over: Partial<BoardCardDTO> = {}): BoardCardDTO => ({
  taskId: id,
  title,
  mode: "code",
  agent: "demo",
  status: "running",
  column: "working",
  repoNames: ["shop-api"],
  pendingProposals: 0,
  usage,
  updatedAt: "2026-01-05T10:00:00Z",
  ...over,
});

const board = (cards: BoardCardDTO[]): BoardResponse => ({ maxAgents: 2, active: 1, hint: { raiseTo: 0 }, cards });

function renderBoard(handler: (c: Call) => Reply | undefined) {
  const api = stubApi(handler);
  FakeSocket.instances = [];
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/board"] }));
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
  return api;
}

const serve = (b: () => BoardResponse) => (c: Call): Reply | undefined => (c.path === "/api/board" ? { body: b() } : undefined);

describe("Agent board", () => {
  it("places cards by the server column and never shows drafts", async () => {
    renderBoard(
      serve(() =>
        board([
          card("1", "Fix cart total"),
          card("2", "Review login", { status: "needs_you", column: "needs_you" }),
          card("3", "Add search", { status: "review", column: "review" }),
          card("4", "Old task", { status: "done", column: "done" }),
          card("5", "Secret draft", { status: "draft", column: "working" }),
        ]),
      ),
    );
    const working = await screen.findByRole("region", { name: "Working" });
    expect(within(working).getByText("Fix cart total")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "Needs you" })).getByText("Review login")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "Review" })).getByText("Add search")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "Done" })).getByText("Old task")).toBeTruthy();
    expect(screen.queryByText("Secret draft")).toBeNull();
  });

  it("shows empty states per column", async () => {
    renderBoard(serve(() => board([])));
    expect(await screen.findByText("Nothing is waiting for you.")).toBeTruthy();
    expect(screen.getByText("No finished work to review.")).toBeTruthy();
  });

  it("shows why a queued task waits, mode warnings, proposals and usage", async () => {
    renderBoard(
      serve(() =>
        board([
          card("1", "Waiting task", { status: "queued", column: "working", blockedBy: "Waiting for Fix cart total to finish", mode: "project", pendingProposals: 2 }),
          card("2", "No cost", { usage: { ...usage, costKnown: false } }),
        ]),
      ),
    );
    expect(await screen.findByText("Waiting: Waiting for Fix cart total to finish")).toBeTruthy();
    expect(screen.getByText("Warning: Whole project")).toBeTruthy();
    expect(screen.getByText("2 proposals")).toBeTruthy();
    expect(screen.getByText(/1\.5k tokens, \$0\.0044/)).toBeTruthy();
    expect(screen.getByText(/cost not reported/)).toBeTruthy();
  });

  it("shows an error with a retry", async () => {
    renderBoard((c) => (c.path === "/api/board" ? { status: 500, body: { code: "internal", message: "The board is unavailable.", hint: "Try again." } } : undefined));
    expect((await screen.findByRole("alert")).textContent).toContain("The board is unavailable.");
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });

  it("calls the cancel endpoint", async () => {
    const { calls } = renderBoard((c) => {
      if (c.path === "/api/board") return { body: board([card("t1", "Fix cart total")]) };
      if (c.path === "/api/tasks/t1/cancel") return { body: { task: { id: "t1" } } };
      return undefined;
    });
    fireEvent.click(await screen.findByRole("button", { name: "Cancel Fix cart total" }));
    await waitFor(() => {
      expect(calls.some((c) => c.method === "POST" && c.path === "/api/tasks/t1/cancel")).toBe(true);
    });
  });

  it("refetches when the board topic reports a change", async () => {
    let data = board([card("1", "First")]);
    renderBoard(serve(() => data));
    await screen.findByText("First");
    data = board([card("1", "First"), card("2", "Second")]);
    act(() => {
      FakeSocket.last().open();
      FakeSocket.last().receive({ type: "event", topic: "board", seq: 1, data: { taskId: "2" } });
    });
    expect(await screen.findByText("Second")).toBeTruthy();
  });

  it("moves focus between cards with the arrow keys", async () => {
    renderBoard(
      serve(() =>
        board([
          card("1", "One"),
          card("2", "Two"),
          card("3", "Three", { status: "review", column: "review" }),
        ]),
      ),
    );
    const one = (await screen.findByText("One")).closest("a") as HTMLElement;
    one.focus();
    fireEvent.keyDown(one, { key: "ArrowDown" });
    expect(document.activeElement?.textContent).toBe("Two");
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "ArrowRight" });
    expect(document.activeElement?.textContent).toBe("Three");
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: "ArrowLeft" });
    expect(document.activeElement?.textContent).toBe("One");
  });

  it("renders titles as plain text", async () => {
    renderBoard(serve(() => board([card("1", '<img src=x onerror="alert(1)"><b>bold</b>')])));
    expect(await screen.findByText(/onerror/)).toBeTruthy();
    expect(document.querySelector("img")).toBeNull();
    expect(document.querySelector("b")).toBeNull();
  });
});
