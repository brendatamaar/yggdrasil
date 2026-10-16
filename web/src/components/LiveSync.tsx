import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { TopicTasks } from "../api/gen/api.gen";
import { taskKeys } from "../api/queries";
import { parseTaskStatus } from "../api/taskStatus";
import { useWS } from "../api/wsContext";
import { useNotificationStore } from "../stores/notifications";

// Statuses a person wants to hear about (UI-06). Running and queued are noise.
const notifiable = new Set(["needs_you", "review", "done", "failed", "cancelled"]);

/**
 * Keeps task queries fresh from the "tasks" topic and turns finished tasks
 * into notifications. A notification holds the task title and its status, and
 * nothing else (docs/AGENTS.md 5.9). Renders nothing.
 */
export function LiveSync() {
  const ws = useWS();
  const queryClient = useQueryClient();
  const notify = useNotificationStore((s) => s.notify);

  useEffect(
    () =>
      ws.subscribe(TopicTasks, {
        onEvent: (msg) => {
          const m = parseTaskStatus(msg.data);
          if (!m) return;
          void queryClient.invalidateQueries({ queryKey: taskKeys.all });
          if (notifiable.has(m.status)) notify(m.title, m.status);
        },
        onGap: () => {
          void queryClient.invalidateQueries({ queryKey: taskKeys.all });
        },
      }),
    [ws, queryClient, notify],
  );

  return null;
}
