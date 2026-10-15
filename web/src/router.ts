import { createRouter, type RouterHistory } from "@tanstack/react-router";
import { indexRoute } from "./routes/index";
import { projectsRoute } from "./routes/projects";
import { rootRoute } from "./routes/root";
import { settingsRoute } from "./routes/settings";
import { taskRoute } from "./routes/tasks.placeholder";

const routeTree = rootRoute.addChildren([indexRoute, taskRoute, projectsRoute, settingsRoute]);

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
