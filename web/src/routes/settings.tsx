import { createRoute } from "@tanstack/react-router";
import { rootRoute } from "./root";
import { SettingsPage } from "./settingsPage";

export const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/settings",
  component: SettingsPage,
});
