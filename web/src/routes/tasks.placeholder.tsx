import { createRoute } from "@tanstack/react-router";
import { Placeholder } from "../components/Placeholder";
import { rootRoute } from "./root";

// The transcript task replaces this file with web/src/routes/tasks/.
export const taskRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/tasks/$taskId",
  component: function TaskPlaceholder() {
    const { taskId } = taskRoute.useParams();
    return <Placeholder title="Task">{`Task ${taskId}`}</Placeholder>;
  },
});
