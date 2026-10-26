import { createRouter, type RouterHistory } from "@tanstack/react-router";
import { indexRoute } from "./routes/index";
import { projectsRoute } from "./routes/projects";
import { rootRoute } from "./routes/root";
import { settingsRoute } from "./routes/settings";
import { boardRoute } from "./routes/board/route";
import { buildRoute, buildsRoute } from "./routes/builds/route";
import { mrsRoute } from "./routes/mrs/route";
import { repoRoute } from "./routes/repos/route";
import { searchRoute } from "./routes/search/route";
import { taskRoute } from "./routes/tasks/route";

const routeTree = rootRoute.addChildren([
  indexRoute,
  taskRoute,
  boardRoute,
  projectsRoute,
  repoRoute,
  buildsRoute,
  buildRoute,
  mrsRoute,
  searchRoute,
  settingsRoute,
]);

/** Creates the app router. Tests pass a memory history. */
export function createAppRouter(history?: RouterHistory) {
  return createRouter({ routeTree, history });
}

export type AppRouter = ReturnType<typeof createAppRouter>;

declare module "@tanstack/react-router" {
  interface Register {
    router: AppRouter;
  }
}
