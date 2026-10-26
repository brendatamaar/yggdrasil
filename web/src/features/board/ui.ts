import { create } from "zustand";
import type { BoardCardDTO } from "../../api/gen/api.gen";
import { formatCost, formatDuration, formatTokens } from "../transcript/format";

/** The four board columns, in display order (AG-12). */
export const columns = [
  { id: "working", title: "Working", empty: "No agent is working right now. Start a task and it shows up here." },
  { id: "needs_you", title: "Needs you", empty: "Nothing is waiting for you." },
  { id: "review", title: "Review", empty: "No finished work to review." },
  { id: "done", title: "Done", empty: "Nothing is done yet." },
] as const;

export type ColumnId = (typeof columns)[number]["id"];

/** Groups cards by the server's column value. Drafts and unknown columns are never shown. */
export function groupCards(cards: readonly BoardCardDTO[]): Record<ColumnId, BoardCardDTO[]> {
  const out: Record<ColumnId, BoardCardDTO[]> = { working: [], needs_you: [], review: [], done: [] };
  for (const c of cards) {
    if (c.status === "draft") continue;
    if (c.column in out) out[c.column as ColumnId].push(c);
  }
  return out;
}

export function modeLabel(mode: string): string {
  switch (mode) {
    case "ask":
      return "Ask";
    case "code":
      return "Code";
    case "current_branch":
      return "Current branch";
    case "project":
      return "Whole project";
    default:
      return mode;
  }
}

/** Modes that edit a main checkout or every repo get the warning style. */
export const isWarnMode = (mode: string) => mode === "current_branch" || mode === "project";

export function statusText(c: BoardCardDTO): string {
  switch (c.status) {
    case "queued":
      return c.blockedBy ? `Waiting: ${c.blockedBy}` : "Waiting for a free slot";
    case "running":
      return "Working";
    case "needs_you":
      return "Needs you";
    case "review":
      return "Ready for review";
    case "done":
      return "Done";
    case "failed":
      return "Failed";
    case "cancelled":
      return "Cancelled";
    default:
      return c.status;
  }
}

export const canCancel = (c: BoardCardDTO) => c.status === "queued" || c.status === "running" || c.status === "needs_you";

export const elapsedText = (c: BoardCardDTO) => (c.usage.durationMs > 0 ? formatDuration(c.usage.durationMs) : "no time reported");

export function tokenTotal(c: BoardCardDTO): number {
  const u = c.usage;
  return u.inputTokens + u.outputTokens + u.cacheWriteTokens + u.cacheReadTokens;
}

export const costText = (u: { costKnown: boolean; costMicroUsd: number }) =>
  u.costKnown ? formatCost(u.costMicroUsd / 1_000_000) : "cost not reported";

export const usageText = (c: BoardCardDTO) => `${formatTokens(tokenTotal(c))} tokens, ${costText(c.usage)}`;

interface BoardUI {
  doneCollapsed: boolean;
  toggleDone: () => void;
}

/** UI-only state of the board page. Server data stays in TanStack Query. */
export const useBoardUI = create<BoardUI>((set) => ({
  doneCollapsed: false,
  toggleDone: () => {
    set((s) => ({ doneCollapsed: !s.doneCollapsed }));
  },
}));
