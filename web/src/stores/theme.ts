import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";

// UI-only state: the user's theme preference. Server data never goes here.

export type Theme = "system" | "dark" | "light";
export type ResolvedTheme = Exclude<Theme, "system">;

export const THEME_STORAGE_KEY = "ygg-theme";

const order: readonly Theme[] = ["system", "dark", "light"];

interface ThemeState {
  theme: Theme;
  setTheme: (theme: Theme) => void;
  cycleTheme: () => void;
}

export const useThemeStore = create<ThemeState>()(
  persist(
    (set, get) => ({
      theme: "dark",
      setTheme: (theme) => {
        set({ theme });
      },
      cycleTheme: () => {
        const next = order[(order.indexOf(get().theme) + 1) % order.length];
        set({ theme: next ?? "dark" });
      },
    }),
    {
      name: THEME_STORAGE_KEY,
      storage: createJSONStorage(() => localStorage),
      partialize: (state) => ({ theme: state.theme }),
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
  if (theme !== "system") return theme;
  return systemPrefersDark() ? "dark" : "light";
}

/** Sets data-theme and the `dark` class on <html>. */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  const resolved = resolveTheme(theme);
  root.dataset.theme = resolved;
  root.classList.toggle("dark", resolved === "dark");
  root.style.colorScheme = resolved;
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
