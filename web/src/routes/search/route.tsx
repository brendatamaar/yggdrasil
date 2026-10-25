import { createRoute } from "@tanstack/react-router";
import { SearchPage } from "../../features/search/SearchPage";
import { rootRoute } from "../root";

export const searchRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/search",
  component: SearchPage,
});
