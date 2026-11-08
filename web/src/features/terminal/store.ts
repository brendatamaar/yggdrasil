import { useEffect } from "react";
import { create } from "zustand";

// UI-only state: whether the dock is open and which repo or worktree the current page offers.
// Connection state lives in the dock's session, not here.

export type TerminalTarget =
  | { kind: "repo"; id: string; label: string }
  | { kind: "worktree"; id: string; label: string };

interface TerminalUiState {
  open: boolean;
  target: TerminalTarget | null;
  setOpen: (open: boolean) => void;
  toggle: () => void;
  setTarget: (target: TerminalTarget | null) => void;
}

export const useTerminalStore = create<TerminalUiState>()((set) => ({
  open: false,
  target: null,
  setOpen: (open) => {
    set({ open });
  },
  toggle: () => {
    set((s) => ({ open: !s.open }));
  },
  setTarget: (target) => {
    set({ target });
  },
}));

/** A page calls this to say which folder the terminal opens in while the page is shown. */
export function useTerminalTarget(kind: TerminalTarget["kind"], id: string | undefined, label: string): void {
  const setTarget = useTerminalStore((s) => s.setTarget);
  useEffect(() => {
    if (!id) return;
    setTarget({ kind, id, label });
    return () => {
      // Only clear our own target; a page that mounted later may have set another.
      const cur = useTerminalStore.getState().target;
      if (cur?.kind === kind && cur.id === id) setTarget(null);
    };
  }, [kind, id, label, setTarget]);
}
