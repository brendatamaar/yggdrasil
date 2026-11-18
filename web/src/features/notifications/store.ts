import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  initialDelivery,
  stepDelivery,
  type DeliveryCommand,
  type DeliveryInput,
  type DeliveryState,
  type Permission,
} from "./core";

// UI-only state (docs/AGENTS.md 5.10). Neither store holds notification history: that is TanStack
// Query data. They hold IDs the person has read, and the browser-delivery machine's state.

const MAX_READ = 400;

interface ReadState {
  /** IDs of notices the person has seen, kept in the browser so a reload does not make them unread. */
  readIds: string[];
  markRead: (ids: readonly string[]) => void;
}

export const useNoticeReadStore = create<ReadState>()(
  persist(
    (set) => ({
      readIds: [],
      markRead: (ids) => {
        set((s) => {
          const have = new Set(s.readIds);
          const add = ids.filter((id) => !have.has(id));
          return add.length === 0 ? s : { readIds: [...s.readIds, ...add].slice(-MAX_READ) };
        });
      },
    }),
    { name: "ygg-notices-read", storage: createJSONStorage(() => localStorage), partialize: (s) => ({ readIds: s.readIds }) },
  ),
);

// --- Browser delivery: the reducer's state plus the one place its commands run ---

/** The permission the browser reports, read without asking. A missing or throwing API is unsupported. */
export function readPermission(): Permission {
  try {
    if (typeof Notification === "undefined") return "unsupported";
    const p = Notification.permission;
    return p === "granted" || p === "denied" ? p : "default";
  } catch {
    return "unsupported";
  }
}

interface DeliveryStore {
  state: DeliveryState;
  dispatch: (input: DeliveryInput) => void;
}

function run(c: DeliveryCommand, dispatch: (i: DeliveryInput) => void): void {
  switch (c.tag) {
    case "requestPermission":
      // Reached only from a PermissionGesture input (a click). The browser's answer comes back as an input.
      try {
        void Promise.resolve(Notification.requestPermission())
          .then((p) => {
            dispatch({ tag: "permissionResolved", permission: p === "granted" || p === "denied" ? p : "default" });
          })
          .catch(() => {
            dispatch({ tag: "permissionResolved", permission: "default" });
          });
      } catch {
        dispatch({ tag: "permissionResolved", permission: "default" });
      }
      return;
    case "showBrowserNotice":
      // The notice stays in the in-app list whatever happens here.
      try {
        new Notification(c.title, { body: c.body, tag: c.id });
      } catch {
        dispatch({ tag: "deliveryFailed", id: c.id });
      }
      return;
  }
}

export const useDeliveryStore = create<DeliveryStore>()((set, get) => ({
  state: initialDelivery(),
  dispatch: (input) => {
    const [next, cmds] = stepDelivery(get().state, input);
    set({ state: next });
    cmds.forEach((c) => {
      run(c, get().dispatch);
    });
  },
}));
