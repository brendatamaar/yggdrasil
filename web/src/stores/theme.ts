import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";

// UI-only state: the user's theme preference. Server data never goes here.
// Phase 2 adds Daylight, Dracula, Rosé Pine, Nord and Gruvbox (UI-05).

export type Theme = "system" | "harbour";
/** The palette actually painted: System resolves to Harbour (OS dark) or light. */
export type ResolvedTheme = "harbour" | "light";

export const THEME_STORAGE_KEY = "ygg-theme";

const order: readonly Theme[] = ["system", "harbour"];

interface ThemeState {
  theme: Theme;
  setTheme: (theme: Theme) => void;
  cycleTheme: () => void;
}

export const useThemeStore = create<ThemeState>()(
  persist(
    (set, get) => ({
      theme: "harbour",
      setTheme: (theme) => {
        set({ theme });
      },
      cycleTheme: () => {
        const next = order[(order.indexOf(get().theme) + 1) % order.length];
        set({ theme: next ?? "harbour" });
      },
    }),
    {
      name: THEME_STORAGE_KEY,
      version: 1,
      storage: createJSONStorage(() => localStorage),
      partialize: (state) => ({ theme: state.theme }),
      // v0 stored "dark" | "light" | "system".
      migrate: (persisted) => {
        const old = (persisted as { theme?: string } | null)?.theme;
        return { theme: old === "dark" || old === "harbour" ? "harbour" : "system" };
      },
    },
  ),
);

const darkQuery = "(prefers-color-scheme: dark)";

function systemPrefersDark(): boolean {
  // matchMedia is missing in some test environments; fall back to dark.
  return typeof window.matchMedia === "function"
    ? window.matchMedia(darkQuery).matches
    : true;
}

export function resolveTheme(theme: Theme): ResolvedTheme {
  if (theme === "harbour") return "harbour";
  return systemPrefersDark() ? "harbour" : "light";
}

/** Sets data-theme and the `dark` class on <html>. Tokens live in index.css. */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  const resolved = resolveTheme(theme);
  root.dataset.theme = resolved;
  root.classList.toggle("dark", resolved === "harbour");
  root.style.colorScheme = resolved === "harbour" ? "dark" : "light";
}

/**
 * Applies the current theme and keeps <html> in sync with the store and, for
 * "system", with the OS setting. Returns a function that stops syncing.
 */
export function syncTheme(): () => void {
  applyTheme(useThemeStore.getState().theme);
  const unsubscribe = useThemeStore.subscribe((state) => {
    applyTheme(state.theme);
  });

  if (typeof window.matchMedia !== "function") return unsubscribe;
  const media = window.matchMedia(darkQuery);
  const onChange = () => {
    if (useThemeStore.getState().theme === "system") applyTheme("system");
  };
  media.addEventListener("change", onChange);
  return () => {
    unsubscribe();
    media.removeEventListener("change", onChange);
  };
}
