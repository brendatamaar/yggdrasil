import { createRoute } from "@tanstack/react-router";
import { Placeholder } from "../components/Placeholder";
import { rootRoute } from "./root";

export const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/settings",
  component: () => <Placeholder title="Settings" />,
});
