import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory } from "@tanstack/react-router";
import { act, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "../../App";
import { ANCHOR_AGENT } from "../../api/queries";
import { WSClient } from "../../api/ws";
import type { BoardCardDTO, ProposalDTO } from "../../api/gen/api.gen";
import { createAppRouter } from "../../router";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";
import { renderWidget } from "../../test/renderWidget";
import { ProposalsPanel } from "../proposals";
import { RecentChats } from "../../widgets/recent-chats/RecentChats";
import { withoutAnchors } from "./api";

// The internal draft "Workspace action proposals" (agent ygg-anchor) only holds proposals. It is
// never a task the person manages: no list, no board column, no Start button. Its proposals are
// still listed by task in the proposal views.

const usage = { inputTokens: 0, outputTokens: 0, cacheWriteTokens: 0, cacheReadTokens: 0, costMicroUsd: 0, costKnown: false, durationMs: 0 };
const card = (id: string, title: string, over: Partial<BoardCardDTO> = {}): BoardCardDTO => ({
  taskId: id,
  title,
  mode: "ask",
  agent: "demo",
  status: "needs_you",
  column: "needs_you",
  repoNames: ["shop-api"],
  pendingProposals: 0,
  usage,
  updatedAt: "2026-01-05T10:00:00Z",
  ...over,
});
const anchor = (): BoardCardDTO =>
  card("anchor1", "Workspace action proposals", { agent: ANCHOR_AGENT, status: "needs_you", column: "needs_you", pendingProposals: 1, updatedAt: "2026-01-06T10:00:00Z" });
const cards = () => [card("1", "Fix cart total"), anchor()];

const serve = (c: Call): Reply | undefined =>
  c.path === "/api/board" ? { body: { maxAgents: 2, active: 0, hint: { raiseTo: 0 }, cards: cards() } } : undefined;

describe("anchor drafts are hidden from task lists", () => {
  it("withoutAnchors drops only the anchor agent", () => {
    expect(withoutAnchors(cards()).map((c) => c.taskId)).toEqual(["1"]);
  });

  it("the board shows no anchor card and no Start button", async () => {
    stubApi(serve);
    FakeSocket.instances = [];
    const router = createAppRouter(createMemoryHistory({ initialEntries: ["/board"] }));
    await act(() => {
      render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
      return Promise.resolve();
    });
    const needsYou = await screen.findByRole("region", { name: "Needs you" });
    expect(within(needsYou).getByText("Fix cart total")).toBeTruthy();
    expect(screen.queryByText("Workspace action proposals")).toBeNull();
    expect(screen.queryByRole("button", { name: /start/i })).toBeNull();
  });

  it("the recent chats list shows no anchor", async () => {
    stubApi(serve);
    await renderWidget(RecentChats);
    expect((await screen.findAllByRole("link")).map((l) => l.textContent)).toEqual(["Fix cart total"]);
  });

  it("the proposals an anchor holds stay visible in the proposal views", async () => {
    const held: ProposalDTO = {
      id: "pr9",
      taskId: "anchor1",
      repoId: "r1",
      repoName: "shop-api",
      kind: "pull_ff",
      status: "pending",
      source: "user",
      fromAgent: false,
      summary: "",
      lines: ["Pull shop-api (fast-forward)"],
      actionHash: "f".repeat(64),
      attempt: 0,
      createdAt: "2026-01-06T10:00:00Z",
    };
    stubApi((c) => (c.path === "/api/proposals?task=anchor1" ? { body: { proposals: [held] } } : undefined));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ProposalsPanel taskId="anchor1" />
      </QueryClientProvider>,
    );
    expect(await screen.findByRole("button", { name: "Review and confirm" })).toBeTruthy();
    expect(screen.getByText(/shop-api/)).toBeTruthy();
  });
});
