import { apiFetch } from "./client";
import type {
  BranchesResponse,
  ChangesResponse,
  CommitDetailResponse,
  DiffResponse,
  LogResponse,
} from "./gen/api.gen";

const enc = encodeURIComponent;
const base = (id: string) => `/api/repos/${enc(id)}`;

export type DiffMode = "unstaged" | "staged";

export const fetchChanges = (id: string) => apiFetch<ChangesResponse>(`${base(id)}/changes`);
export const fetchDiff = (id: string, path: string, mode: DiffMode) =>
  apiFetch<DiffResponse>(`${base(id)}/diff?path=${enc(path)}&mode=${mode}`);
export const fetchLog = (id: string, limit: number) => apiFetch<LogResponse>(`${base(id)}/log?limit=${String(limit)}`);
export const fetchCommit = (id: string, oid: string) =>
  apiFetch<CommitDetailResponse>(`${base(id)}/commits/${enc(oid)}`);
export const fetchBranches = (id: string) => apiFetch<BranchesResponse>(`${base(id)}/branches`);
