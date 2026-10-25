import type { WidgetModule } from "../types";
import { NeedsAttention } from "./NeedsAttention";

export const needsAttention: WidgetModule = {
  id: "needs-attention",
  title: "Needs attention",
  defaultSize: { w: 6, h: 4, minW: 3, minH: 2 },
  Component: NeedsAttention,
};
