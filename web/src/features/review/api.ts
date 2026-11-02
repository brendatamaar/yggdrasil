import { ApiError, apiFetch } from "../../api/client";
import type {
  CreateNoteRequest,
  ReviewNoteDTO,
  ReviewNotesResponse,
  SendFailureResponse,
  SendNotesResponse,
  VerificationDTO,
  VerificationsResponse,
} from "../../api/gen/api.gen";

const enc = encodeURIComponent;
const task = (id: string) => `/api/tasks/${enc(id)}`;
const post = (body?: unknown): RequestInit => ({ method: "POST", body: body === undefined ? undefined : JSON.stringify(body) });

export const fetchVerifications = (taskId: string) => apiFetch<VerificationsResponse>(`${task(taskId)}/verifications`);
export const retryVerification = (taskId: string) => apiFetch<VerificationsResponse>(`${task(taskId)}/verify/retry`, post());
export const cancelVerification = (id: string) => apiFetch<VerificationDTO>(`/api/verifications/${enc(id)}/cancel`, post());
export const sendFailure = (id: string) => apiFetch<SendFailureResponse>(`/api/verifications/${enc(id)}/send-failure`, post());

export const fetchNotes = (taskId: string) => apiFetch<ReviewNotesResponse>(`${task(taskId)}/notes`);
export const createNote = (taskId: string, req: CreateNoteRequest) => apiFetch<ReviewNoteDTO>(`${task(taskId)}/notes`, post(req));
export const editNote = (id: string, text: string) =>
  apiFetch<ReviewNoteDTO>(`/api/notes/${enc(id)}`, { method: "PUT", body: JSON.stringify({ text }) });
export const deleteNote = (id: string) => apiFetch<undefined>(`/api/notes/${enc(id)}`, { method: "DELETE" });
export const sendNotes = (taskId: string, ids: string[]) => apiFetch<SendNotesResponse>(`${task(taskId)}/notes/send`, post({ ids }));

/** True for the 501 "not ready" answer of an endpoint that is not wired yet. */
export const isNotReady = (e: unknown) => e instanceof ApiError && e.status === 501;

/**
 * The task's worktree diff for one repo, as unified-diff text (all files).
 * BACKEND GAP: no endpoint serves a task worktree's diff yet (GET
 * /api/repos/{id}/diff reads the main checkout). Until the Go side adds one
 * this answers "not ready", and the panel shows that state.
 */
export type DiffLoader = (taskId: string, repoId: string) => Promise<{ text: string; truncated: boolean; binary: boolean }>;
export const loadWorktreeDiff: DiffLoader = () =>
  Promise.reject(new ApiError(501, { code: "not_ready", message: "Worktree diffs aren't available yet." }));
