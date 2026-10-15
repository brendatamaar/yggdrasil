import { useEffect } from "react";
import { create } from "zustand";

// UI-only state: the command palette registry (UI-04). Any module can add commands.

export interface Command {
  id: string;
  title: string;
  /** Section heading in the palette. */
  group?: string;
  /** Extra search terms. */
  keywords?: string[];
  run: () => void;
}

interface CommandState {
  commands: Record<string, Command>;
  open: boolean;
  setOpen: (open: boolean) => void;
}

export const useCommandStore = create<CommandState>()((set) => ({
  commands: {},
  open: false,
  setOpen: (open) => {
    set({ open });
  },
}));

/** Adds commands (same id replaces). Returns a function that removes them. */
export function registerCommands(commands: Command[]): () => void {
  useCommandStore.setState((s) => ({
    commands: { ...s.commands, ...Object.fromEntries(commands.map((c) => [c.id, c])) },
  }));
  return () => {
    useCommandStore.setState((s) => ({
      // Keep ids another module has since replaced.
      commands: Object.fromEntries(Object.entries(s.commands).filter(([, c]) => !commands.includes(c))),
    }));
  };
}

/** Registers commands while the calling component is mounted. Pass a stable array. */
export function useRegisterCommands(commands: Command[]): void {
  useEffect(() => registerCommands(commands), [commands]);
}
