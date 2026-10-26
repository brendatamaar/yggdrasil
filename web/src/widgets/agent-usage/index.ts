import type { WidgetModule } from "../types";
import { AgentUsage } from "./AgentUsage";

export const agentUsage: WidgetModule = {
  id: "agent-usage",
  title: "Agent usage",
  defaultSize: { w: 6, h: 3, minW: 3, minH: 2 },
  Component: AgentUsage,
};
