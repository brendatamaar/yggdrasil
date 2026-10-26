import { agentUsage } from "./agent-usage";
import { builds } from "./builds";
import { ciFailures } from "./ci-failures";
import { gitActivity } from "./git-activity";
import { mergeRequests } from "./merge-requests";
import { needsAttention } from "./needs-attention";
import { recentAreas } from "./recent-areas";
import { recentChats } from "./recent-chats";
import { thisMachine } from "./this-machine";
import type { WidgetModule } from "./types";
import { workingNow } from "./working-now";

/**
 * Every widget the home page can show. Adding a widget means adding its
 * folder and one line here; the layout and the "Add widget" menu follow.
 */
export const widgetRegistry: readonly WidgetModule[] = [
  workingNow,
  thisMachine,
  needsAttention,
  gitActivity,
  mergeRequests,
  ciFailures,
  builds,
  recentAreas,
  recentChats,
  agentUsage,
];

export function getWidget(id: string): WidgetModule | undefined {
  return widgetRegistry.find((w) => w.id === id);
}
