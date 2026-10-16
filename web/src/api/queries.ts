import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type { Event as AgentEvent } from "./gen/agents.gen";
import type {
  CreateTaskRequest,
  HealthResponse,
  TaskEventsResponse,
  TaskListResponse,
  TaskResponse,
} from "./gen/api.gen";
import type { Task } from "./gen/core.gen";

// Server state goes through TanStack Query (docs/AGENTS.md 5.10).

export const taskKeys = {
  /** Prefix of every task query: invalidating it refreshes them all. */
  all: ["tasks"] as const,
  running: ["tasks", "list", "running"] as const,
  one: (id: string) => ["tasks", "one", id] as const,
};

const taskPath = (id: string) => `/api/tasks/${encodeURIComponent(id)}`;

export function useRunningTasks() {
  return useQuery({
    queryKey: taskKeys.running,
    queryFn: async (): Promise<Task[]> => (await apiFetch<TaskListResponse>("/api/tasks?status=running")).tasks,
  });
}

export function useTask(id: string) {
  return useQuery({
    queryKey: taskKeys.one(id),
    queryFn: async (): Promise<Task> => (await apiFetch<TaskResponse>(taskPath(id))).task,
  });
}

export function useHealth() {
  return useQuery({
    queryKey: ["health"],
    queryFn: () => apiFetch<HealthResponse>("/api/health"),
  });
}

/** Events of a task after `after` (a Seq), oldest first. */
export async function fetchTaskEvents(id: string, after: number): Promise<AgentEvent[]> {
  return (await apiFetch<TaskEventsResponse>(`${taskPath(id)}/events?after=${String(after)}`)).events;
}

export async function createTask(req: CreateTaskRequest): Promise<Task> {
  return (
    await apiFetch<TaskResponse>("/api/tasks", { method: "POST", body: JSON.stringify(req) })
  ).task;
}

export async function cancelTask(id: string): Promise<Task> {
  return (await apiFetch<TaskResponse>(`${taskPath(id)}/cancel`, { method: "POST" })).task;
}
