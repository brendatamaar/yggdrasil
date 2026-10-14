import { createRouter, type RouterHistory } from "@tanstack/react-router";
import { indexRoute } from "./routes/index";
import { rootRoute } from "./routes/root";

const routeTree = rootRoute.addChildren([indexRoute]);

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
