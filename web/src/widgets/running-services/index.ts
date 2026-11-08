import type { WidgetModule } from "../types";
import { RunningServices } from "./RunningServices";

export const runningServices: WidgetModule = {
  id: "running-services",
  title: "Running services",
  defaultSize: { w: 6, h: 4, minW: 3, minH: 2 },
  Component: RunningServices,
};
