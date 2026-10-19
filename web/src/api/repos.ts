import { apiFetch } from "./client";
import type {
  DirListingResponse,
  PickDTO,
  PreviewResponse,
  RefreshReposRequest,
  RenameProjectRequest,
  ReposResponse,
  RescanResponse,
  TrackProjectRequest,
  TrackReposRequest,
  TrackResponse,
  UntrackResponse,
} from "./gen/api.gen";

export type RepoFilter = "all" | "uncommitted" | "behind" | "ahead" | "failed_pipeline" | "problems";

const post = (body: unknown): RequestInit => ({ method: "POST", body: JSON.stringify(body) });
const enc = encodeURIComponent;

export const fetchDirs = (path: string) => apiFetch<DirListingResponse>(`/api/fs/dirs?path=${enc(path)}`);
export const previewProject = (path: string) => apiFetch<PreviewResponse>("/api/projects/preview", post({ path }));
export const trackProject = (req: TrackProjectRequest) => apiFetch<TrackResponse>("/api/projects", post(req));
export const trackRepos = (projectId: string, picks: PickDTO[]) =>
  apiFetch<TrackResponse>(`/api/projects/${enc(projectId)}/repos`, post({ picks } satisfies TrackReposRequest));
export const rescanProject = (projectId: string) =>
  apiFetch<RescanResponse>(`/api/projects/${enc(projectId)}/rescan`, { method: "POST" });
export const untrackProject = (projectId: string) =>
  apiFetch<UntrackResponse>(`/api/projects/${enc(projectId)}/untrack`, { method: "POST" });
export const untrackRepo = (repoId: string) =>
  apiFetch<UntrackResponse>(`/api/repos/${enc(repoId)}/untrack`, { method: "POST" });
export const renameProject = (projectId: string, name: string) =>
  apiFetch<unknown>(`/api/projects/${enc(projectId)}`, {
    method: "PATCH",
    body: JSON.stringify({ name } satisfies RenameProjectRequest),
  });
export const fetchRepos = (filter: RepoFilter) => apiFetch<ReposResponse>(`/api/repos?filter=${enc(filter)}`);
export const refreshRepos = (req: RefreshReposRequest) => apiFetch<unknown>("/api/repos/refresh", post(req));
