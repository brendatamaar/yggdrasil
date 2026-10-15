import { createRoute } from "@tanstack/react-router";
import { Placeholder } from "../components/Placeholder";
import { rootRoute } from "./root";

export const projectsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/projects",
  component: () => <Placeholder title="Projects" />,
});
