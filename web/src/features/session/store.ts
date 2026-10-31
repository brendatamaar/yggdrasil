import { create } from "zustand";

/** The UI session: either usable, or the server asked for the launch link (SF-01). */
export type SessionState = { tag: "ok" } | { tag: "required" };

interface SessionStore {
  state: SessionState;
  markRequired: () => void;
}

export const useSessionStore = create<SessionStore>((set) => ({
  state: { tag: "ok" },
  markRequired: () => {
    set({ state: { tag: "required" } });
  },
}));

/** True when an error response means "open Yggdrasil from the terminal link". */
export function isSessionRequired(status: number, code: string): boolean {
  return status === 401 && code === "session_required";
}
