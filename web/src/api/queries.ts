import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type { Event as AgentEvent } from "./gen/agents.gen";
import type {
  CreateTaskRequest,
  HealthResponse,
  TaskEventsResponse,
  TaskListResponse,
  TaskResponse,
  VerificationSummaryDTO,
} from "./gen/api.gen";
import type { Task } from "./gen/core.gen";

// Server state goes through TanStack Query (docs/AGENTS.md 5.10).

export const taskKeys = {
  /** Prefix of every task query: invalidating it refreshes them all. */
  all: ["tasks"] as const,
  running: ["tasks", "list", "running"] as const,
  one: (id: string) => ["tasks", "one", id] as const,
};

/**
 * The agent of the internal draft that only holds a project's workspace-action proposals. It is
 * not a task the person made and cannot be started, so task lists and the board never show it;
 * the proposals it holds stay visible in the proposal views.
 */
export const ANCHOR_AGENT = "ygg-anchor";
export const isAnchorAgent = (agent: string): boolean => agent === ANCHOR_AGENT;

const taskPath =(id: string) => `/api/tasks/${encodeURIComponent(id)}`;

export function useRunningTasks() {
  return useQuery({
    queryKey: taskKeys.running,
    queryFn: async (): Promise<Task[]> => (await apiFetch<TaskListResponse>("/api/tasks?status=running")).tasks.filter((t) => !isAnchorAgent(t.agent)),
  });
}

/** A task with the verification summary the server sends beside it. */
export type TaskWithVerification = Task & { verification?: VerificationSummaryDTO };

export function useTask(id: string) {
  return useQuery({
    queryKey: taskKeys.one(id),
    queryFn: async (): Promise<TaskWithVerification> => {
      const r = await apiFetch<TaskResponse>(taskPath(id));
      return { ...r.task, verification: r.verification };
    },
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
