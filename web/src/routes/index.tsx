import { createRoute } from "@tanstack/react-router";
import { rootRoute } from "./root";

// The start page is intentionally empty until the first widgets land.
export const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: () => null,
});
