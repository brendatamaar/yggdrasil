import type { WidgetModule } from "../types";
import { CiFailures } from "./CiFailures";

export const ciFailures: WidgetModule = {
  id: "ci-failures",
  title: "CI failures",
  defaultSize: { w: 6, h: 3, minW: 3, minH: 2 },
  Component: CiFailures,
};
