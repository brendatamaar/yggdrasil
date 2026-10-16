import type { WidgetModule } from "../types";
import { WorkingNow } from "./WorkingNow";

export const workingNow: WidgetModule = {
  id: "working-now",
  title: "Working now",
  defaultSize: { w: 6, h: 3, minW: 3, minH: 2 },
  Component: WorkingNow,
};
