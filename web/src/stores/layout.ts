import { create } from "zustand";
import {
  LAYOUT_KEY,
  addWidget,
  defaultLayout,
  parseLayout,
  removeWidget,
  serializeLayout,
  type LayoutItem,
} from "../components/widgetgrid/layout";
import { widgetRegistry } from "../widgets/registry";
import type { WidgetModule } from "../widgets/types";

// UI-only state: the home grid arrangement, saved in local storage (UI-02).

interface LayoutState {
  items: LayoutItem[];
  customizing: boolean;
  setItems: (items: LayoutItem[]) => void;
  add: (mod: WidgetModule) => void;
  remove: (id: string) => void;
  reset: () => void;
  setCustomizing: (on: boolean) => void;
}

function save(items: LayoutItem[]): void {
  try {
    localStorage.setItem(LAYOUT_KEY, serializeLayout(items));
  } catch {
    // Storage full or blocked: the layout just isn't remembered.
  }
}

function load(): LayoutItem[] {
  try {
    return parseLayout(localStorage.getItem(LAYOUT_KEY), widgetRegistry);
  } catch {
    return defaultLayout(widgetRegistry);
  }
}

export const useLayoutStore = create<LayoutState>()((set) => ({
  items: load(),
  customizing: false,
  setItems: (items) => {
    save(items);
    set({ items });
  },
  add: (mod) => {
    set((s) => {
      const items = addWidget(s.items, mod);
      save(items);
      return { items };
    });
  },
  remove: (id) => {
    set((s) => {
      const items = removeWidget(s.items, id);
      save(items);
      return { items };
    });
  },
  reset: () => {
    const items = defaultLayout(widgetRegistry);
    save(items);
    set({ items });
  },
  setCustomizing: (customizing) => {
    set({ customizing });
  },
}));
