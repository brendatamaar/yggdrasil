import { createRoute } from "@tanstack/react-router";
import { rootRoute } from "../root";
import { TaskPage } from "./TaskPage";

export const taskRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/tasks/$taskId",
  component: function TaskRoute() {
    const { taskId } = taskRoute.useParams();
    return <TaskPage taskId={taskId} />;
  },
});
