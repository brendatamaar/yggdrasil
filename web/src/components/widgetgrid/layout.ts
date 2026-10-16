import type { WidgetModule } from "../../widgets/types";

// The saved arrangement of the home grid. Pure functions, so a corrupt or old
// value in local storage can never break the page.

export const GRID_COLUMNS = 12;
export const LAYOUT_KEY = "ygg.layout";
/** Bump when the saved shape changes, and add a migration in parseLayout. */
export const LAYOUT_VERSION = 1;

export interface LayoutItem {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

/** The widgets side by side, wrapping into rows, at their default sizes. */
export function defaultLayout(mods: readonly WidgetModule[]): LayoutItem[] {
  const items: LayoutItem[] = [];
  let x = 0;
  let y = 0;
  let rowH = 0;
  for (const m of mods) {
    const w = clamp(m.defaultSize.w, 1, GRID_COLUMNS);
    if (x + w > GRID_COLUMNS) {
      x = 0;
      y += rowH;
      rowH = 0;
    }
    items.push({ id: m.id, x, y, w, h: Math.max(1, m.defaultSize.h) });
    x += w;
    rowH = Math.max(rowH, m.defaultSize.h);
  }
  return items;
}

function clamp(n: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, n));
}

function isItem(v: unknown): v is LayoutItem {
  if (typeof v !== "object" || v === null) return false;
  const o = v as Record<string, unknown>;
  return (
    typeof o.id === "string" &&
    [o.x, o.y, o.w, o.h].every((n) => typeof n === "number" && Number.isFinite(n))
  );
}

/**
 * Reads a saved layout. Anything it cannot trust gives the default layout:
 * missing value, bad JSON, an unknown version. A version 0 value (a bare list
 * of items, from before versions) is migrated. Items for widgets that no
 * longer exist are dropped, and positions are kept inside the grid.
 */
export function parseLayout(raw: string | null, mods: readonly WidgetModule[]): LayoutItem[] {
  if (raw === null) return defaultLayout(mods);
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return defaultLayout(mods);
  }
  let list: unknown;
  if (Array.isArray(value)) {
    list = value; // version 0
  } else if (typeof value === "object" && value !== null && (value as { version?: unknown }).version === LAYOUT_VERSION) {
    list = (value as { items?: unknown }).items;
  } else {
    return defaultLayout(mods);
  }
  if (!Array.isArray(list)) return defaultLayout(mods);

  const known = new Map(mods.map((m) => [m.id, m]));
  const seen = new Set<string>();
  const items: LayoutItem[] = [];
  for (const v of list as unknown[]) {
    if (!isItem(v) || seen.has(v.id)) continue;
    const mod = known.get(v.id);
    if (!mod) continue;
    seen.add(v.id);
    const w = clamp(Math.round(v.w), mod.defaultSize.minW ?? 1, GRID_COLUMNS);
    items.push({
      id: v.id,
      w,
      h: Math.max(mod.defaultSize.minH ?? 1, Math.round(v.h)),
      x: clamp(Math.round(v.x), 0, GRID_COLUMNS - w),
      y: Math.max(0, Math.round(v.y)),
    });
  }
  // A saved list of only retired widgets is no layout at all; an empty list
  // the person made by removing everything is.
  if (items.length === 0 && list.length > 0) return defaultLayout(mods);
  return items;
}

export function serializeLayout(items: readonly LayoutItem[]): string {
  return JSON.stringify({ version: LAYOUT_VERSION, items });
}

/** Adds a widget below everything else. A widget already on the grid stays where it is. */
export function addWidget(items: readonly LayoutItem[], mod: WidgetModule): LayoutItem[] {
  if (items.some((i) => i.id === mod.id)) return [...items];
  const bottom = items.reduce((max, i) => Math.max(max, i.y + i.h), 0);
  const w = clamp(mod.defaultSize.w, 1, GRID_COLUMNS);
  return [...items, { id: mod.id, x: 0, y: bottom, w, h: Math.max(1, mod.defaultSize.h) }];
}

export function removeWidget(items: readonly LayoutItem[], id: string): LayoutItem[] {
  return items.filter((i) => i.id !== id);
}

/** Whether two layouts place the same widgets in the same cells, whatever their order. */
export function sameLayout(a: readonly LayoutItem[], b: readonly LayoutItem[]): boolean {
  if (a.length !== b.length) return false;
  const byId = new Map(b.map((i) => [i.id, i]));
  return a.every((x) => {
    const y = byId.get(x.id);
    return y !== undefined && x.x === y.x && x.y === y.y && x.w === y.w && x.h === y.h;
  });
}
