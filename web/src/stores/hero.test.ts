import { beforeEach, describe, expect, it, vi } from "vitest";
import { HERO_STORAGE_KEY, phaseAt, resolveHeroPhase, useHeroStore } from "./hero";

const at = (h: number, m = 0) => new Date(2026, 0, 15, h, m);

describe("phaseAt", () => {
  it.each([
    [0, "night"],
    [4, "night"],
    [5, "dawn"],
    [7, "dawn"],
    [8, "day"],
    [16, "day"],
    [17, "dusk"],
    [19, "dusk"],
    [20, "night"],
    [23, "night"],
  ] as const)("hour %i is %s", (h, phase) => {
    expect(phaseAt(at(h))).toBe(phase);
  });

  it("switches exactly on the minute boundary", () => {
    expect(phaseAt(at(7, 59))).toBe("dawn");
    expect(phaseAt(at(8, 0))).toBe("day");
  });
});

describe("resolveHeroPhase", () => {
  it("follows the clock in auto mode", () => {
    expect(resolveHeroPhase("auto", at(18))).toBe("dusk");
  });
  it("a fixed mode overrides the clock", () => {
    expect(resolveHeroPhase("night", at(12))).toBe("night");
    expect(resolveHeroPhase("dawn", at(23))).toBe("dawn");
  });
  it("off paints nothing", () => {
    expect(resolveHeroPhase("off", at(12))).toBeNull();
  });
});

describe("hero store", () => {
  beforeEach(() => {
    localStorage.clear();
    useHeroStore.setState({ mode: "auto" });
  });

  it("persists the mode", () => {
    useHeroStore.getState().setMode("dusk");
    const raw = JSON.parse(localStorage.getItem(HERO_STORAGE_KEY) ?? "null") as { state: { mode: string } };
    expect(raw.state.mode).toBe("dusk");
  });

  it("restores a stored mode and ignores an unknown one", async () => {
    localStorage.setItem(HERO_STORAGE_KEY, JSON.stringify({ state: { mode: "night" }, version: 1 }));
    await useHeroStore.persist.rehydrate();
    expect(useHeroStore.getState().mode).toBe("night");

    localStorage.setItem(HERO_STORAGE_KEY, JSON.stringify({ state: { mode: "sunrise" }, version: 1 }));
    await useHeroStore.persist.rehydrate();
    expect(useHeroStore.getState().mode).toBe("night");
  });

  it("keeps working when storage throws", () => {
    const set = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(() => {
      useHeroStore.getState().setMode("day");
    }).not.toThrow();
    expect(useHeroStore.getState().mode).toBe("day");
    set.mockRestore();
  });
});
