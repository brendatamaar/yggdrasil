import { createRoute } from "@tanstack/react-router";
import { ProjectsPage } from "../features/repos/ProjectsPage";
import { rootRoute } from "./root";

export const projectsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/projects",
  component: ProjectsPage,
});
