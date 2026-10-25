import type { WidgetModule } from "../types";
import { GitActivity } from "./GitActivity";

export const gitActivity: WidgetModule = {
  id: "git-activity",
  title: "Git activity",
  defaultSize: { w: 6, h: 4, minW: 3, minH: 2 },
  Component: GitActivity,
};
