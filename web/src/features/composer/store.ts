import { create } from "zustand";
import type { Mode } from "./core";

// UI-only state: whether the server refuses write modes, and which risky
// modes this person already confirmed in this session. Never the prompt.
interface ComposerUi {
  writeModesOff: boolean;
  acknowledged: readonly Mode[];
  hideWriteModes: () => void;
  acknowledge: (mode: Mode) => void;
}

export const useComposerUi = create<ComposerUi>((set) => ({
  writeModesOff: false,
  acknowledged: [],
  hideWriteModes: () => {
    set({ writeModesOff: true });
  },
  acknowledge: (mode) => {
    set((s) => (s.acknowledged.includes(mode) ? s : { acknowledged: [...s.acknowledged, mode] }));
  },
}));
