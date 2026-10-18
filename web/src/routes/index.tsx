import { createRoute } from "@tanstack/react-router";
import { HomePage } from "../components/hero/HomePage";
import { rootRoute } from "./root";

export const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: HomePage,
});
