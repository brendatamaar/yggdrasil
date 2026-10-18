import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";

// UI-only state: the user's theme preference. Server data never goes here.
// UI-05: System plus six palettes; tokens live in src/styles/themes.css.

export const THEMES = ["system", "harbour", "daylight", "dracula", "rose-pine", "nord", "gruvbox"] as const;
export type Theme = (typeof THEMES)[number];
/** The palette actually painted: System resolves to Harbour (OS dark) or light. */
export type ResolvedTheme = Exclude<Theme, "system">;

export const THEME_LABELS: Record<Theme, string> = {
  system: "System",
  harbour: "Harbour",
  daylight: "Daylight",
  dracula: "Dracula",
  "rose-pine": "Rosé Pine",
  nord: "Nord",
  gruvbox: "Gruvbox",
};

const LIGHT_THEMES: readonly ResolvedTheme[] = ["daylight"];

export const THEME_STORAGE_KEY = "ygg-theme";

const order: readonly Theme[] = THEMES;

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
      version: 2,
      storage: createJSONStorage(() => localStorage),
      partialize: (state) => ({ theme: state.theme }),
      // v0 stored "dark" | "light" | "system"; v1 "harbour" | "system".
      migrate: (persisted) => {
        const old = (persisted as { theme?: string } | null)?.theme;
        if (old === "dark") return { theme: "harbour" };
        return { theme: isTheme(old) ? old : "system" };
      },
    },
  ),
);

export function isTheme(value: unknown): value is Theme {
  return typeof value === "string" && (THEMES as readonly string[]).includes(value);
}

const darkQuery = "(prefers-color-scheme: dark)";

function systemPrefersDark(): boolean {
  // matchMedia is missing in some test environments; fall back to dark.
  return typeof window.matchMedia === "function"
    ? window.matchMedia(darkQuery).matches
    : true;
}

export function resolveTheme(theme: Theme): ResolvedTheme {
  if (theme === "system") return systemPrefersDark() ? "harbour" : "daylight";
  return theme;
}

/** Sets data-theme, the `dark` class and color-scheme on <html>. Tokens live in styles/themes.css. */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  const resolved = resolveTheme(theme);
  const dark = !LIGHT_THEMES.includes(resolved);
  root.dataset.theme = resolved;
  root.classList.toggle("dark", dark);
  root.style.colorScheme = dark ? "dark" : "light";
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
