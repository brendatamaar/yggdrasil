import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BoardCardDTO } from "../../api/gen/api.gen";
import { stubApi } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { AgentUsage } from "./AgentUsage";
import { usageRows } from "./usage";

const card = (id: string, agent: string, tokens: number, costKnown: boolean, updatedAt = "2026-01-05T12:00:00"): BoardCardDTO => ({
  taskId: id, title: id, mode: "code", agent, status: "done", column: "done", repoNames: [], pendingProposals: 0, updatedAt,
  usage: { inputTokens: tokens, outputTokens: 0, cacheWriteTokens: 0, cacheReadTokens: 0, costMicroUsd: costKnown ? 2_000_000 : 0, costKnown, durationMs: 0 },
});
const serve = (cards: BoardCardDTO[]) => stubApi((c) => (c.path === "/api/board" ? { body: { maxAgents: 2, active: 0, hint: { raiseTo: 0 }, cards } } : undefined));

describe("usageRows", () => {
  it("sums per day and agent and marks unknown cost", () => {
    const rows = usageRows([card("a", "demo", 100, true), card("b", "demo", 50, false), card("c", "other", 10, true)]);
    expect(rows).toHaveLength(2);
    const demo = rows.find((r) => r.agent === "demo");
    expect(demo).toMatchObject({ tokens: 150, costKnown: false });
  });
});

describe("Agent usage widget", () => {
  it("shows a table with cost or 'not reported'", async () => {
    serve([card("a", "demo", 1500, true), card("b", "other", 300, false)]);
    await renderWidget(AgentUsage);
    const table = await screen.findByRole("table");
    expect(within(table).getByText("$2.0000")).toBeTruthy();
    expect(within(table).getByText("not reported")).toBeTruthy();
    expect(within(table).getByText("1.5k")).toBeTruthy();
  });

  it("has empty and error states", async () => {
    serve([]);
    const { unmount } = await renderWidget(AgentUsage);
    expect(await screen.findByText(/No usage yet/)).toBeTruthy();
    unmount();
    stubApi(() => ({ status: 500, body: { code: "internal", message: "Board down.", hint: "" } }));
    await renderWidget(AgentUsage);
    expect((await screen.findByRole("alert")).textContent).toContain("Board down.");
  });
});
