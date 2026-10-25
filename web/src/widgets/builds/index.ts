import type { WidgetModule } from "../types";
import { Builds } from "./Builds";

export const builds: WidgetModule = {
  id: "builds",
  title: "Builds",
  defaultSize: { w: 6, h: 4, minW: 3, minH: 2 },
  Component: Builds,
};
