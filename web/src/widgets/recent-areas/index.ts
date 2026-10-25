import type { WidgetModule } from "../types";
import { RecentAreas } from "./RecentAreas";

export const recentAreas: WidgetModule = {
  id: "recent-areas",
  title: "Recent areas",
  defaultSize: { w: 6, h: 3, minW: 3, minH: 2 },
  Component: RecentAreas,
};
