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
    useThemeStore.setState({ theme: "dark" });
  });

  it("cycles dark, light, system and persists each step", () => {
    const { cycleTheme } = useThemeStore.getState();

    cycleTheme();
    expect(useThemeStore.getState().theme).toBe("light");
    expect(storedTheme()).toBe("light");

    cycleTheme();
    expect(useThemeStore.getState().theme).toBe("system");
    expect(storedTheme()).toBe("system");

    cycleTheme();
    expect(useThemeStore.getState().theme).toBe("dark");
    expect(storedTheme()).toBe("dark");
  });

  it("restores the persisted theme on rehydrate", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, JSON.stringify({ state: { theme: "light" }, version: 0 }));

    await useThemeStore.persist.rehydrate();

    expect(useThemeStore.getState().theme).toBe("light");
  });

  it("applies the resolved theme to <html>", () => {
    const root = document.createElement("html");

    applyTheme("light", root);
    expect(root.dataset.theme).toBe("light");
    expect(root.classList.contains("dark")).toBe(false);

    applyTheme("dark", root);
    expect(root.dataset.theme).toBe("dark");
    expect(root.classList.contains("dark")).toBe(true);
  });
});
