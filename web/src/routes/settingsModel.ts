// Pure helpers of the Settings page: no IO.
import type { BoardCardDTO, MaxAgentsResponse } from "../api/gen/api.gen";
import { costText } from "../features/board";
import { formatTokens } from "../features/transcript/format";

export interface UsageTotals {
  tokens: number;
  costMicroUsd: number;
  /** False when any task reported no cost: the sum is then a lower bound. */
  allCostKnown: boolean;
  tasks: number;
}

export function usageTotals(cards: readonly BoardCardDTO[]): UsageTotals {
  let tokens = 0;
  let costMicroUsd = 0;
  let allCostKnown = true;
  for (const c of cards) {
    const u = c.usage;
    tokens += u.inputTokens + u.outputTokens + u.cacheWriteTokens + u.cacheReadTokens;
    if (u.costKnown) costMicroUsd += u.costMicroUsd;
    else allCostKnown = false;
  }
  return { tokens, costMicroUsd, allCostKnown, tasks: cards.length };
}

/** AG-15: tokens and cost across the tasks on the board; unknown cost is said, not shown as zero. */
export function usageLine(t: UsageTotals): string {
  if (t.tasks === 0) return "No tasks yet.";
  const cost = t.allCostKnown
    ? costText({ costKnown: true, costMicroUsd: t.costMicroUsd })
    : t.costMicroUsd > 0
      ? `at least ${costText({ costKnown: true, costMicroUsd: t.costMicroUsd })} (some agents report no cost)`
      : "cost not reported";
  return `${formatTokens(t.tokens)} tokens across ${String(t.tasks)} ${t.tasks === 1 ? "task" : "tasks"}, ${cost}`;
}

/** The room hint in words, or null when there is no suggestion. */
export function roomHintText(hint: { raiseTo: number }, current: number): string | null {
  return hint.raiseTo > current ? `Every slot is busy and this machine has room. Try ${String(hint.raiseTo)} agents at once.` : null;
}

/** Why the typed value cannot be saved, or null. */
export function maxAgentsError(value: string, s: Pick<MaxAgentsResponse, "min" | "max">): string | null {
  const n = Number(value);
  if (value.trim() === "" || !Number.isInteger(n) || n < s.min || n > s.max) {
    return `Use a whole number from ${String(s.min)} to ${String(s.max)}.`;
  }
  return null;
}
