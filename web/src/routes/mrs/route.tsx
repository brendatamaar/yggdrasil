import { createRoute } from "@tanstack/react-router";
import { Placeholder } from "../../components/Placeholder";
import { rootRoute } from "../root";

// Owned by the MR inbox task (GH-02).
export const mrsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/mrs",
  component: () => <Placeholder title="Merge requests" />,
});
