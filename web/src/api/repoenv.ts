import { ApiError, apiFetch } from "./client";
import type { RepoEnvDTO, WorktreeEnvDTO } from "./gen/api.gen";

const enc = encodeURIComponent;
const post: RequestInit = { method: "POST" };

export const fetchRepoEnv = (repoId: string) => apiFetch<RepoEnvDTO>(`/api/repos/${enc(repoId)}/env`);
export const saveRepoEnv = (env: RepoEnvDTO) =>
  apiFetch<RepoEnvDTO>(`/api/repos/${enc(env.repoId)}/env`, { method: "PUT", body: JSON.stringify(env) });
export const fetchWorktreeEnv = (worktreeId: string) => apiFetch<WorktreeEnvDTO>(`/api/worktrees/${enc(worktreeId)}/env`);
export const copyProfile = (worktreeId: string) =>
  apiFetch<WorktreeEnvDTO>(`/api/worktrees/${enc(worktreeId)}/profile/copy`, post);

export type ComposeAction = "confirm" | "up" | "stop" | "down";
/** "confirm" must carry the composeHash the user was shown; the server refuses it without one. */
export const runComposeAction = (worktreeId: string, action: ComposeAction, hash?: string) =>
  apiFetch<WorktreeEnvDTO>(
    `/api/worktrees/${enc(worktreeId)}/compose/${action}`,
    action === "confirm" ? { ...post, body: JSON.stringify({ hash: hash ?? "" }) } : post,
  );

/** The server answers 501 until compose is available: the UI shows "not available yet". */
export const isNotAvailable = (e: unknown) => e instanceof ApiError && e.status === 501;
