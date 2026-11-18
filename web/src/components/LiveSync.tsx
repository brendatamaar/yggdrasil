import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { TopicTasks } from "../api/gen/api.gen";
import { taskKeys } from "../api/queries";
import { parseTaskStatus } from "../api/taskStatus";
import { useWS } from "../api/wsContext";

/**
 * Keeps task queries fresh from the "tasks" topic. Notifications are not made here any more: the
 * server commits a notice and the "notices" topic delivers it (features/notifications, UI-06).
 * Renders nothing.
 */
export function LiveSync() {
  const ws = useWS();
  const queryClient = useQueryClient();

  useEffect(
    () =>
      ws.subscribe(TopicTasks, {
        onEvent: (msg) => {
          if (!parseTaskStatus(msg.data)) return;
          void queryClient.invalidateQueries({ queryKey: taskKeys.all });
        },
        onGap: () => {
          void queryClient.invalidateQueries({ queryKey: taskKeys.all });
        },
      }),
    [ws, queryClient],
  );

  return null;
}
