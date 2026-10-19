import { createRoute } from "@tanstack/react-router";
import { Placeholder } from "../../components/Placeholder";
import { rootRoute } from "../root";

// Owned by the builds UI task (BL-03, BL-05–07).
export const buildsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/builds",
  component: () => <Placeholder title="Builds" />,
});

export const buildRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/builds/$buildId",
  component: () => <Placeholder title="Build" />,
});
