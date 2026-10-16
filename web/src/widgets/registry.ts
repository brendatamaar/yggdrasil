import { thisMachine } from "./this-machine";
import type { WidgetModule } from "./types";
import { workingNow } from "./working-now";

/**
 * Every widget the home page can show. Adding a widget means adding its
 * folder and one line here; the layout and the "Add widget" menu follow.
 */
export const widgetRegistry: readonly WidgetModule[] = [workingNow, thisMachine];

export function getWidget(id: string): WidgetModule | undefined {
  return widgetRegistry.find((w) => w.id === id);
}
