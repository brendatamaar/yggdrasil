import { beforeEach, describe, expect, it, vi } from "vitest";
import { applyTheme, resolveTheme, THEME_STORAGE_KEY, THEMES, useThemeStore } from "./theme";

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

  it("cycles through every theme and wraps, persisting each step", () => {
    const { cycleTheme } = useThemeStore.getState();
    const seen: string[] = [];
    for (let i = 0; i < THEMES.length; i++) {
      cycleTheme();
      seen.push(useThemeStore.getState().theme);
      expect(storedTheme()).toBe(useThemeStore.getState().theme);
    }
    expect(seen).toEqual([...THEMES.slice(2), "system", "harbour"]);
  });

  it("sets and persists any theme", () => {
    for (const theme of THEMES) {
      useThemeStore.getState().setTheme(theme);
      expect(storedTheme()).toBe(theme);
    }
  });

  it("falls back to system for an unknown persisted value", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify({ state: { theme: "solarized" }, version: 1 }));
    await useThemeStore.persist.rehydrate();
    expect(useThemeStore.getState().theme).toBe("system");
  });

  it("keeps a valid v1 value", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify({ state: { theme: "nord" }, version: 1 }));
    await useThemeStore.persist.rehydrate();
    expect(useThemeStore.getState().theme).toBe("nord");
  });

  it("marks only Daylight as a light theme", () => {
    for (const theme of THEMES) {
      if (theme === "system") continue;
      const root = document.createElement("html");
      applyTheme(theme, root);
      expect(root.dataset.theme).toBe(theme);
      expect(root.classList.contains("dark")).toBe(theme !== "daylight");
    }
  });

  it("resolves system by prefers-color-scheme", () => {
    try {
      for (const [dark, expected] of [[true, "harbour"], [false, "daylight"]] as const) {
        vi.stubGlobal("matchMedia", (q: string) => ({ matches: dark, media: q }));
        expect(resolveTheme("system")).toBe(expected);
      }
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("restores the persisted theme on rehydrate", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify({ state: { theme: "system" }, version: 2 }));

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
