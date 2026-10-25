import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { render } from "@testing-library/react";
import type { ComponentType } from "react";

/** Renders a widget with a query client and a router that knows the pages widgets link to. */
export async function renderWidget(Component: ComponentType) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const root = createRootRoute({ component: Outlet });
  const stub = () => null;
  const tree = root.addChildren([
    createRoute({ getParentRoute: () => root, path: "/", component: () => <Component /> }),
    createRoute({ getParentRoute: () => root, path: "/repos/$repoId", component: stub }),
    createRoute({ getParentRoute: () => root, path: "/builds/$buildId", component: stub }),
    createRoute({ getParentRoute: () => root, path: "/builds", component: stub }),
    createRoute({ getParentRoute: () => root, path: "/mrs", component: stub }),
    createRoute({ getParentRoute: () => root, path: "/projects", component: stub }),
  ]);
  const router = createRouter({ routeTree: tree, history: createMemoryHistory({ initialEntries: ["/"] }) });
  await router.load();
  return render(
    <QueryClientProvider client={client}>
      {/* The test router has its own route tree, not the app's registered one. */}
      <RouterProvider router={router as unknown as ReturnType<typeof import("../router").createAppRouter>} />
    </QueryClientProvider>,
  );
}
