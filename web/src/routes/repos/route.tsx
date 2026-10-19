import { createRoute } from "@tanstack/react-router";
import { RepoPage } from "../../features/repodetail/RepoPage";
import { rootRoute } from "../root";

// WS-05 and GH-02. Git actions (WS-06 to WS-08) are added by a later task.
export const repoRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/repos/$repoId",
  component: RepoPage,
});
