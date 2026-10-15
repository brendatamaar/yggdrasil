import { create } from "zustand";

// UI-only state: the in-app notification list (UI-06). Text is only the task
// name and status (docs/AGENTS.md 5.9), so the item type has no other fields.

export interface AppNotification {
  id: number;
  taskName: string;
  /** A core.TaskStatus value. */
  status: string;
  read: boolean;
}

const statusLabels: Record<string, string> = {
  needs_you: "Needs you",
  review: "Ready for review",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
  running: "Running",
  queued: "Queued",
  draft: "Draft",
};

export function statusLabel(status: string): string {
  return statusLabels[status] ?? status;
}

interface NotificationState {
  items: AppNotification[];
  notify: (taskName: string, status: string) => void;
  markAllRead: () => void;
  clear: () => void;
}

let nextId = 1;

export const useNotificationStore = create<NotificationState>()((set) => ({
  items: [],
  notify: (taskName, status) => {
    set((s) => ({ items: [{ id: nextId++, taskName, status, read: false }, ...s.items] }));
  },
  markAllRead: () => {
    set((s) => ({ items: s.items.map((i) => ({ ...i, read: true })) }));
  },
  clear: () => {
    set({ items: [] });
  },
}));
