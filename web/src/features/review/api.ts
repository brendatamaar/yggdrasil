import { ApiError, apiFetch } from "../../api/client";
import type {
  CreateNoteRequest,
  ReviewNoteDTO,
  ReviewNotesResponse,
  SendFailureResponse,
  SendNotesResponse,
  VerificationDTO,
  VerificationsResponse,
  WorktreeDiffDTO,
} from "../../api/gen/api.gen";

const enc = encodeURIComponent;
const task = (id: string) => `/api/tasks/${enc(id)}`;
const post = (body?: unknown): RequestInit => ({ method: "POST", body: body === undefined ? undefined : JSON.stringify(body) });

export const fetchVerifications = (taskId: string) => apiFetch<VerificationsResponse>(`${task(taskId)}/verifications`);
export const retryVerification = (taskId: string) => apiFetch<VerificationsResponse>(`${task(taskId)}/verify/retry`, post());
export const cancelVerification = (id: string) => apiFetch<VerificationDTO>(`/api/verifications/${enc(id)}/cancel`, post());
export const sendFailure = (id: string, optIn = false) =>
  apiFetch<SendFailureResponse>(`/api/verifications/${enc(id)}/send-failure`, post(optIn ? { optIn: true } : undefined));

export const fetchNotes = (taskId: string) => apiFetch<ReviewNotesResponse>(`${task(taskId)}/notes`);
export const createNote = (taskId: string, req: CreateNoteRequest) => apiFetch<ReviewNoteDTO>(`${task(taskId)}/notes`, post(req));
export const editNote = (id: string, text: string) =>
  apiFetch<ReviewNoteDTO>(`/api/notes/${enc(id)}`, { method: "PUT", body: JSON.stringify({ text }) });
export const deleteNote = (id: string) => apiFetch<undefined>(`/api/notes/${enc(id)}`, { method: "DELETE" });
export const sendNotes = (taskId: string, ids: string[], optIn = false) =>
  apiFetch<SendNotesResponse>(`${task(taskId)}/notes/send`, post(optIn ? { ids, optIn: true } : { ids }));

/** True for the 501 "not ready" answer of an endpoint that is not wired yet. */
export const isNotReady = (e: unknown) => e instanceof ApiError && e.status === 501;

/** The task's worktree diff for one repo (GET /api/tasks/{id}/repos/{repo}/diff): all files, with per-file hashes. */
export type DiffLoader = (taskId: string, repoId: string) => Promise<WorktreeDiffDTO>;
export const loadWorktreeDiff: DiffLoader = (taskId, repoId) =>
  apiFetch<WorktreeDiffDTO>(`${task(taskId)}/repos/${enc(repoId)}/diff`);
