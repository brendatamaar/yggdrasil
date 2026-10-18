import { create } from "zustand";
import { createJSONStorage, persist, type StateStorage } from "zustand/middleware";

// UI-01: the hero scene's time-of-day preference. UI-only state.

export const HERO_MODES = ["auto", "dawn", "day", "dusk", "night", "off"] as const;
export type HeroMode = (typeof HERO_MODES)[number];
export type HeroPhase = "dawn" | "day" | "dusk" | "night";

export const HERO_STORAGE_KEY = "ygg-hero";

export const HERO_MODE_LABELS: Record<HeroMode, string> = {
  auto: "Follow time of day",
  dawn: "Dawn",
  day: "Day",
  dusk: "Dusk",
  night: "Night",
  off: "Off",
};

/** Local-time phase: dawn 05-08, day 08-17, dusk 17-20, night otherwise. */
export function phaseAt(date: Date): HeroPhase {
  const h = date.getHours();
  if (h >= 5 && h < 8) return "dawn";
  if (h >= 8 && h < 17) return "day";
  if (h >= 17 && h < 20) return "dusk";
  return "night";
}

/** The phase to paint, or null when the scene is off. */
export function resolveHeroPhase(mode: HeroMode, now: Date): HeroPhase | null {
  if (mode === "off") return null;
  if (mode === "auto") return phaseAt(now);
  return mode;
}

export function isHeroMode(value: unknown): value is HeroMode {
  return typeof value === "string" && (HERO_MODES as readonly string[]).includes(value);
}

// localStorage can throw (blocked, quota, private mode); the preference then
// just lives in memory.
const safeStorage: StateStorage = {
  getItem: (name) => {
    try {
      return localStorage.getItem(name);
    } catch {
      return null;
    }
  },
  setItem: (name, value) => {
    try {
      localStorage.setItem(name, value);
    } catch {
      // Not persisted.
    }
  },
  removeItem: (name) => {
    try {
      localStorage.removeItem(name);
    } catch {
      // Nothing to remove.
    }
  },
};

interface HeroState {
  mode: HeroMode;
  setMode: (mode: HeroMode) => void;
}

export const useHeroStore = create<HeroState>()(
  persist(
    (set) => ({
      mode: "auto",
      setMode: (mode) => {
        set({ mode });
      },
    }),
    {
      name: HERO_STORAGE_KEY,
      version: 1,
      storage: createJSONStorage(() => safeStorage),
      partialize: (state) => ({ mode: state.mode }),
      merge: (persisted, current) => {
        const mode = (persisted as { mode?: unknown } | null)?.mode;
        return { ...current, mode: isHeroMode(mode) ? mode : current.mode };
      },
    },
  ),
);
