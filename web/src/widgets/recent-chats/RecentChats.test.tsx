import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BoardCardDTO } from "../../api/gen/api.gen";
import { stubApi } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { RecentChats } from "./RecentChats";

const usage = { inputTokens: 0, outputTokens: 0, cacheWriteTokens: 0, cacheReadTokens: 0, costMicroUsd: 0, costKnown: false, durationMs: 0 };
const card = (id: string, title: string, updatedAt: string, status = "done"): BoardCardDTO => ({
  taskId: id, title, mode: "code", agent: "demo", status, column: "done", repoNames: [], pendingProposals: 0, usage, updatedAt,
});
const serve = (cards: BoardCardDTO[]) => stubApi((c) => (c.path === "/api/board" ? { body: { maxAgents: 2, active: 0, hint: { raiseTo: 0 }, cards } } : undefined));

describe("Recent chats widget", () => {
  it("lists newest first, skips drafts and shows the status", async () => {
    serve([card("1", "Older", "2026-01-01T00:00:00Z"), card("2", "Newer", "2026-01-02T00:00:00Z", "failed"), card("3", "Draft", "2026-01-03T00:00:00Z", "draft")]);
    await renderWidget(RecentChats);
    const links = await screen.findAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(["Newer", "Older"]);
    expect(screen.getByText("Failed")).toBeTruthy();
  });

  it("has an empty state", async () => {
    serve([]);
    await renderWidget(RecentChats);
    expect(await screen.findByText(/No chats yet/)).toBeTruthy();
  });

  it("has an error state", async () => {
    stubApi(() => ({ status: 500, body: { code: "internal", message: "Board down.", hint: "" } }));
    await renderWidget(RecentChats);
    expect((await screen.findByRole("alert")).textContent).toContain("Board down.");
  });
});
