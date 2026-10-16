import type { TaskStatusMessage } from "./gen/api.gen";

/** Reads the server's TaskStatusMessage; anything else is ignored. */
export function parseTaskStatus(data: unknown): TaskStatusMessage | null {
  if (typeof data !== "object" || data === null) return null;
  const d = data as Record<string, unknown>;
  if (typeof d.taskId !== "string" || typeof d.title !== "string" || typeof d.status !== "string") return null;
  return { taskId: d.taskId, title: d.title, status: d.status };
}
