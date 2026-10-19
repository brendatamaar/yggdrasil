import { createRoute } from "@tanstack/react-router";
import { MrInboxPage } from "../../features/mrs/MrInboxPage";
import { rootRoute } from "../root";

export const mrsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/mrs",
  component: MrInboxPage,
});
