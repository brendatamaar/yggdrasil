import { createRoute } from "@tanstack/react-router";
import { Placeholder } from "../../components/Placeholder";
import { rootRoute } from "../root";

// Owned by the repo page task (WS-05, WS-06–08 actions).
export const repoRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/repos/$repoId",
  component: () => <Placeholder title="Repo" />,
});
