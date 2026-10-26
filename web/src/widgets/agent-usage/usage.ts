import type { BoardCardDTO } from "../../api/gen/api.gen";

export interface UsageRow {
  day: string;
  agent: string;
  tokens: number;
  costMicroUsd: number;
  /** False when at least one task of the row has no reported cost. */
  costKnown: boolean;
}

/** Local calendar day of an ISO time, "2026-01-05"; "" when unparseable. */
function dayOf(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${String(d.getFullYear())}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** Sums tokens and cost per day (by last update) and agent, newest day first. */
export function usageRows(cards: readonly BoardCardDTO[]): UsageRow[] {
  const map = new Map<string, UsageRow>();
  for (const c of cards) {
    const day = dayOf(c.updatedAt);
    const key = `${day}\u0000${c.agent}`;
    const u = c.usage;
    const tokens = u.inputTokens + u.outputTokens + u.cacheWriteTokens + u.cacheReadTokens;
    const row = map.get(key) ?? { day, agent: c.agent, tokens: 0, costMicroUsd: 0, costKnown: true };
    row.tokens += tokens;
    if (u.costKnown) row.costMicroUsd += u.costMicroUsd;
    else if (tokens > 0) row.costKnown = false;
    map.set(key, row);
  }
  return [...map.values()].sort((a, b) => b.day.localeCompare(a.day) || a.agent.localeCompare(b.agent));
}
