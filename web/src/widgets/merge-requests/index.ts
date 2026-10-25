import type { WidgetModule } from "../types";
import { MergeRequests } from "./MergeRequests";

export const mergeRequests: WidgetModule = {
  id: "merge-requests",
  title: "Merge requests",
  defaultSize: { w: 6, h: 4, minW: 3, minH: 2 },
  Component: MergeRequests,
};
