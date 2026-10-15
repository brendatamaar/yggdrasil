import { createRoute } from "@tanstack/react-router";
import { Placeholder } from "../components/Placeholder";
import { rootRoute } from "./root";

// The start page is a placeholder until the first widgets land.
export const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: () => <Placeholder title="Start" />,
});
