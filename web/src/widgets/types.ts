import type { ComponentType } from "react";

// The widget module interface (UI-02). A widget is a self-contained folder
// under src/widgets/<name>/ that exports one WidgetModule. Widgets must not
// import from each other (docs/AGENTS.md 5.10): the only file a widget imports
// from this folder is this one. The registry lists the modules.

/** Size on the 12-column home grid. */
export interface WidgetSize {
  w: number;
  h: number;
  minW?: number;
  minH?: number;
}

/** An action a widget offers, for the widget's menu and later for the command palette. */
export interface WidgetAction {
  id: string;
  label: string;
  run: () => void;
}

export interface WidgetModule {
  /** Stable ID, stored in the saved layout. Never reuse one for another widget. */
  id: string;
  title: string;
  defaultSize: WidgetSize;
  Component: ComponentType;
  actions?: readonly WidgetAction[];
}
