import { createRoute } from "@tanstack/react-router";
import { BuildPage } from "../../features/builds/BuildPage";
import { BuildsPage } from "../../features/builds/BuildsPage";
import { rootRoute } from "../root";

export const buildsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/builds",
  component: BuildsPage,
});

export const buildRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/builds/$buildId",
  component: function BuildRouteComponent() {
    const { buildId } = buildRoute.useParams();
    return <BuildPage buildId={buildId} />;
  },
});
