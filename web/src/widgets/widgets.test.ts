import { describe, expect, it } from "vitest";
import { GRID_COLUMNS } from "../components/widgetgrid/layout";
import { getWidget, widgetRegistry } from "./registry";

describe("widget registry", () => {
  it("lists widgets with unique, stable IDs", () => {
    const ids = widgetRegistry.map((w) => w.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const id of ids) expect(id).toMatch(/^[a-z][a-z0-9-]*$/);
    expect(ids).toContain("working-now");
    expect(ids).toContain("this-machine");
  });

  it("gives every widget a title and a size that fits the grid", () => {
    for (const w of widgetRegistry) {
      expect(w.title.trim()).not.toBe("");
      expect(w.defaultSize.w).toBeGreaterThanOrEqual(1);
      expect(w.defaultSize.w).toBeLessThanOrEqual(GRID_COLUMNS);
      expect(w.defaultSize.h).toBeGreaterThanOrEqual(1);
      expect(w.defaultSize.minW ?? 1).toBeLessThanOrEqual(w.defaultSize.w);
      expect(w.defaultSize.minH ?? 1).toBeLessThanOrEqual(w.defaultSize.h);
    }
  });

  it("finds a widget by ID", () => {
    expect(getWidget("working-now")?.title).toBe("Working now");
    expect(getWidget("nope")).toBeUndefined();
  });
});
