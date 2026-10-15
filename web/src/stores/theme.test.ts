import { beforeEach, describe, expect, it } from "vitest";
import { applyTheme, THEME_STORAGE_KEY, useThemeStore } from "./theme";

function storedTheme(): unknown {
  const raw = localStorage.getItem(THEME_STORAGE_KEY);
  if (raw === null) return undefined;
  const parsed = JSON.parse(raw) as { state?: { theme?: unknown } };
  return parsed.state?.theme;
}

describe("theme store", () => {
  beforeEach(() => {
    useThemeStore.setState({ theme: "harbour" });
  });

  it("cycles harbour, system and persists each step", () => {
    const { cycleTheme } = useThemeStore.getState();

    cycleTheme();
    expect(useThemeStore.getState().theme).toBe("system");
    expect(storedTheme()).toBe("system");

    cycleTheme();
    expect(useThemeStore.getState().theme).toBe("harbour");
    expect(storedTheme()).toBe("harbour");
  });

  it("restores the persisted theme on rehydrate", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify({ state: { theme: "system" }, version: 1 }));

    await useThemeStore.persist.rehydrate();

    expect(useThemeStore.getState().theme).toBe("system");
  });

  it("migrates a v0 value", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify({ state: { theme: "dark" }, version: 0 }));
    await useThemeStore.persist.rehydrate();
    expect(useThemeStore.getState().theme).toBe("harbour");

    localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify({ state: { theme: "light" }, version: 0 }));
    await useThemeStore.persist.rehydrate();
    expect(useThemeStore.getState().theme).toBe("system");
  });

  it("applies the resolved theme to <html>", () => {
    const root = document.createElement("html");

    applyTheme("harbour", root);
    expect(root.dataset.theme).toBe("harbour");
    expect(root.classList.contains("dark")).toBe(true);
  });

  it("resolves system to harbour when the OS has no preference API", () => {
    const root = document.createElement("html");
    // jsdom has no matchMedia, so system falls back to the dark palette.
    applyTheme("system", root);
    expect(root.dataset.theme).toBe("harbour");
  });
});
