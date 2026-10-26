import type { WidgetModule } from "../types";
import { RecentChats } from "./RecentChats";

export const recentChats: WidgetModule = {
  id: "recent-chats",
  title: "Recent chats",
  defaultSize: { w: 6, h: 3, minW: 3, minH: 2 },
  Component: RecentChats,
};
