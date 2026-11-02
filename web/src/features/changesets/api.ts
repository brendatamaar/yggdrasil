import { ApiError, apiFetch } from "../../api/client";
import type {
  ChangeSetDTO,
  ConfirmRunRequest,
  MergeLocalRequest,
  PreflightResponse,
  ProposalDTO,
  ScopeDTO,
  SetOrderRequest,
  WorktreesResponse,
} from "../../api/gen/api.gen";

const enc = encodeURIComponent;
const post = (body?: unknown): RequestInit => ({ method: "POST", ...(body === undefined ? {} : { body: JSON.stringify(body) }) });

export const fetchTaskChangeSet = (taskId: string) => apiFetch<ChangeSetDTO>(`/api/tasks/${enc(taskId)}/changeset`);
export const fetchWorktrees = async (taskId: string) => (await apiFetch<WorktreesResponse>(`/api/tasks/${enc(taskId)}/worktrees`)).worktrees;

export const setOrder = (id: string, order: string[]) =>
  apiFetch<ChangeSetDTO>(`/api/changesets/${enc(id)}/order`, { method: "PUT", body: JSON.stringify({ order } satisfies SetOrderRequest) });
export const preflight = (id: string, scope: ScopeDTO) =>
  apiFetch<PreflightResponse>(`/api/changesets/${enc(id)}/preflight`, post({ scope }));
export const confirmRun = (id: string, req: ConfirmRunRequest) => apiFetch<ChangeSetDTO>(`/api/changesets/${enc(id)}/confirm`, post(req));
export const stopRun = (id: string) => apiFetch<ChangeSetDTO>(`/api/changesets/${enc(id)}/stop`, post());
export const closeChangeSet = (id: string) => apiFetch<ChangeSetDTO>(`/api/changesets/${enc(id)}/close`, post());

export const proposeMergeLocal = (worktreeId: string, title?: string) =>
  apiFetch<ProposalDTO>(`/api/worktrees/${enc(worktreeId)}/merge-local`, post((title ? { title } : {}) satisfies MergeLocalRequest));
export const proposeRemoveWorktree = (worktreeId: string) => apiFetch<ProposalDTO>(`/api/worktrees/${enc(worktreeId)}/remove`, post());

/** True for the 501 "not ready" answer of an endpoint that is not wired yet. */
export const isNotReady = (e: unknown) => e instanceof ApiError && e.status === 501;
/** True for a 409 or 400: the server refused with a reason in plain words. */
export const isRefusal = (e: unknown) => e instanceof ApiError && (e.status === 409 || e.status === 400);
