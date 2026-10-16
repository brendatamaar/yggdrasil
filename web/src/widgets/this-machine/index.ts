import type { WidgetModule } from "../types";
import { ThisMachine } from "./ThisMachine";

export const thisMachine: WidgetModule = {
  id: "this-machine",
  title: "This machine",
  defaultSize: { w: 6, h: 3, minW: 3, minH: 2 },
  Component: ThisMachine,
};
