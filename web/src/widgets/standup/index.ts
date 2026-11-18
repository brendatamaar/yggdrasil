import { StandupPanel } from "../../features/standup";
import type { WidgetModule } from "../types";

export const standup: WidgetModule = {
  id: "standup",
  title: "Standup",
  defaultSize: { w: 6, h: 5, minW: 3, minH: 3 },
  Component: StandupPanel,
};
