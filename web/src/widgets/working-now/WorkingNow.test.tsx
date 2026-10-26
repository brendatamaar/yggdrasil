import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BoardCardDTO } from "../../api/gen/api.gen";
import { stubApi } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { WorkingNow } from "./WorkingNow";

const usage = { inputTokens: 0, outputTokens: 0, cacheWriteTokens: 0, cacheReadTokens: 0, costMicroUsd: 0, costKnown: false, durationMs: 125000 };
const card = (id: string, title: string, status: string): BoardCardDTO => ({
  taskId: id, title, mode: "code", agent: "demo", status, column: "working", repoNames: [], pendingProposals: 0, usage, updatedAt: "2026-01-01T00:00:00Z",
});
const serve = (cards: BoardCardDTO[]) => stubApi((c) => (c.path === "/api/board" ? { body: { maxAgents: 2, active: 0, hint: { raiseTo: 0 }, cards } } : undefined));

describe("Working now widget", () => {
  it("shows only running tasks with elapsed time", async () => {
    serve([card("1", "Fix cart total", "running"), card("2", "Queued one", "queued")]);
    await renderWidget(WorkingNow);
    expect(await screen.findByRole("link", { name: "Fix cart total" })).toBeTruthy();
    expect(screen.getByText("2 min 5 s")).toBeTruthy();
    expect(screen.queryByText("Queued one")).toBeNull();
  });

  it("has empty and error states", async () => {
    serve([]);
    const { unmount } = await renderWidget(WorkingNow);
    expect(await screen.findByText("Nothing is running.")).toBeTruthy();
    unmount();
    stubApi(() => ({ status: 500, body: { code: "internal", message: "Board down.", hint: "" } }));
    await renderWidget(WorkingNow);
    expect((await screen.findByRole("alert")).textContent).toContain("Board down.");
  });
});
