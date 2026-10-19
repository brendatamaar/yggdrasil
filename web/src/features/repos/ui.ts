import { useState } from "react";
import type { CandidateDTO } from "../../api/gen/api.gen";

export const btn = "rounded-md border border-line px-3 py-1 text-sm hover:bg-surface disabled:opacity-50";
export const btnPrimary = "rounded-md bg-accent px-3 py-1 text-sm text-canvas hover:opacity-90 disabled:opacity-50";
export const menuContent = "z-10 min-w-40 rounded-md border border-line bg-surface p-1 text-sm shadow-lg";
export const menuItem = "cursor-default rounded-sm px-2 py-1 outline-none data-[highlighted]:bg-canvas";

export function useChecked() {
  const [unchecked, setUnchecked] = useState<Set<string>>(new Set());
  const checked = (cands: CandidateDTO[]) => new Set(cands.filter((c) => !unchecked.has(c.relPath)).map((c) => c.relPath));
  const toggle = (rel: string) => {
    setUnchecked((s) => {
      const n = new Set(s);
      if (n.has(rel)) n.delete(rel);
      else n.add(rel);
      return n;
    });
  };
  return { checked, toggle };
}


export function relativeTime(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return "just now";
  const units: [number, string][] = [
    [60, "minute"],
    [3600, "hour"],
    [86400, "day"],
    [2592000, "month"],
    [31536000, "year"],
  ];
  let best: [number, string] = units[0] as [number, string];
  for (const u of units) if (s >= u[0]) best = u;
  const n = Math.floor(s / best[0]);
  return `${String(n)} ${best[1]}${n === 1 ? "" : "s"} ago`;
}
