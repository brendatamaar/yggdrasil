import { describe, expect, it } from "vitest";
import type { WidgetModule } from "../../widgets/types";
import {
  GRID_COLUMNS,
  LAYOUT_VERSION,
  addWidget,
  defaultLayout,
  parseLayout,
  removeWidget,
  sameLayout,
  serializeLayout,
} from "./layout";

const mod = (id: string, w = 6, h = 3): WidgetModule => ({
  id,
  title: id,
  defaultSize: { w, h, minW: 2, minH: 2 },
  Component: () => null,
});
const mods = [mod("a"), mod("b"), mod("c")];

describe("defaultLayout", () => {
  it("puts widgets side by side and wraps into rows", () => {
    expect(defaultLayout(mods)).toEqual([
      { id: "a", x: 0, y: 0, w: 6, h: 3 },
      { id: "b", x: 6, y: 0, w: 6, h: 3 },
      { id: "c", x: 0, y: 3, w: 6, h: 3 },
    ]);
  });

  it("keeps a too-wide widget inside the grid", () => {
    expect(defaultLayout([mod("wide", 40)])[0]?.w).toBe(GRID_COLUMNS);
  });
});

describe("parseLayout", () => {
  it("round-trips what serializeLayout writes", () => {
    const items = [{ id: "a", x: 1, y: 2, w: 4, h: 5 }];
    expect(parseLayout(serializeLayout(items), mods)).toEqual(items);
  });

  it("falls back to the default for missing, broken or unknown-version values", () => {
    const def = defaultLayout(mods);
    expect(parseLayout(null, mods)).toEqual(def);
    expect(parseLayout("{not json", mods)).toEqual(def);
    expect(parseLayout('"a string"', mods)).toEqual(def);
    expect(parseLayout(JSON.stringify({ version: LAYOUT_VERSION + 1, items: [] }), mods)).toEqual(def);
    expect(parseLayout(JSON.stringify({ version: LAYOUT_VERSION, items: "nope" }), mods)).toEqual(def);
  });

  it("migrates version 0, a bare list of items", () => {
    const v0 = JSON.stringify([{ id: "b", x: 0, y: 0, w: 6, h: 3 }]);
    expect(parseLayout(v0, mods)).toEqual([{ id: "b", x: 0, y: 0, w: 6, h: 3 }]);
  });

  it("drops retired widgets, duplicates and malformed items", () => {
    const raw = JSON.stringify({
      version: LAYOUT_VERSION,
      items: [
        { id: "a", x: 0, y: 0, w: 6, h: 3 },
        { id: "gone", x: 0, y: 3, w: 6, h: 3 },
        { id: "a", x: 6, y: 0, w: 6, h: 3 },
        { id: "b", x: "left", y: 0, w: 6, h: 3 },
        null,
        { id: "c", x: 0, y: 3, w: 6, h: 3 },
      ],
    });
    expect(parseLayout(raw, mods).map((i) => i.id)).toEqual(["a", "c"]);
  });

  it("keeps positions inside the grid and sizes at least their minimum", () => {
    const raw = JSON.stringify({ version: LAYOUT_VERSION, items: [{ id: "a", x: 11, y: -4, w: 99, h: 0 }] });
    const [item] = parseLayout(raw, mods);
    expect(item).toEqual({ id: "a", x: 0, y: 0, w: GRID_COLUMNS, h: 2 });
  });

  it("keeps an empty layout the person made, but not a list of only retired widgets", () => {
    expect(parseLayout(JSON.stringify({ version: LAYOUT_VERSION, items: [] }), mods)).toEqual([]);
    const retired = JSON.stringify({ version: LAYOUT_VERSION, items: [{ id: "gone", x: 0, y: 0, w: 4, h: 3 }] });
    expect(parseLayout(retired, mods)).toEqual(defaultLayout(mods));
  });
});

describe("addWidget and removeWidget", () => {
  it("adds below everything and ignores a widget that is already there", () => {
    const items = [{ id: "a", x: 0, y: 0, w: 6, h: 3 }];
    const added = addWidget(items, mod("b", 4, 2));
    expect(added[1]).toEqual({ id: "b", x: 0, y: 3, w: 4, h: 2 });
    expect(addWidget(added, mod("b"))).toEqual(added);
    expect(items).toHaveLength(1);
  });

  it("removes by id", () => {
    expect(removeWidget(defaultLayout(mods), "b").map((i) => i.id)).toEqual(["a", "c"]);
  });
});

describe("sameLayout", () => {
  it("ignores order but not position", () => {
    const a = defaultLayout(mods);
    expect(sameLayout(a, [...a].reverse())).toBe(true);
    expect(sameLayout(a, a.map((i) => (i.id === "b" ? { ...i, y: 9 } : i)))).toBe(false);
    expect(sameLayout(a, a.slice(1))).toBe(false);
  });
});
