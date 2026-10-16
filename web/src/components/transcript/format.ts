/** "850 ms", "4.2 s", "2 min 5 s". */
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${String(Math.round(ms))} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)} s`;
  const min = Math.floor(s / 60);
  return `${String(min)} min ${String(Math.round(s - min * 60))} s`;
}

/** "950", "12.3k", "1.2M". */
export function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

/** "$0.0044": four decimals, because single runs cost cents or less. */
export function formatCost(usd: number): string {
  return `$${usd.toFixed(4)}`;
}
