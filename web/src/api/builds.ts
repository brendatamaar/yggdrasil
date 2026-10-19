import { apiFetch } from "./client";
import type {
  BuildDTO,
  BuildListResponse,
  BuildPresetDTO,
  BuildPresetListResponse,
  EnqueueBuildRequest,
  EnqueueBuildResponse,
  SaveBuildPresetRequest,
  StepLogResponse,
  UpdateBuildPresetRequest,
} from "./gen/api.gen";

const enc = encodeURIComponent;
const send = (method: string, body?: unknown): RequestInit => ({
  method,
  ...(body === undefined ? {} : { body: JSON.stringify(body) }),
});

export interface BuildFilters {
  project?: string;
  status?: string;
}

export async function fetchBuilds(f: BuildFilters): Promise<BuildDTO[]> {
  const q = new URLSearchParams();
  if (f.project) q.set("project", f.project);
  if (f.status) q.set("status", f.status);
  const qs = q.toString();
  return (await apiFetch<BuildListResponse>(`/api/builds${qs ? `?${qs}` : ""}`)).builds;
}

export const fetchBuild = (id: string) => apiFetch<BuildDTO>(`/api/builds/${enc(id)}`);
export const enqueueBuild = (req: EnqueueBuildRequest) =>
  apiFetch<EnqueueBuildResponse>("/api/builds", send("POST", req));
export const cancelBuild = (id: string) => apiFetch<BuildDTO>(`/api/builds/${enc(id)}/cancel`, send("POST"));
export const fetchStepLog = (id: string, step: number, after: number) =>
  apiFetch<StepLogResponse>(`/api/builds/${enc(id)}/steps/${String(step)}/log?after=${String(after)}`);

export async function fetchPresets(project?: string): Promise<BuildPresetDTO[]> {
  const qs = project ? `?project=${enc(project)}` : "";
  return (await apiFetch<BuildPresetListResponse>(`/api/build-presets${qs}`)).presets;
}
export const createPreset = (req: SaveBuildPresetRequest) =>
  apiFetch<BuildPresetDTO>("/api/build-presets", send("POST", req));
export const updatePreset = (id: string, req: UpdateBuildPresetRequest) =>
  apiFetch<BuildPresetDTO>(`/api/build-presets/${enc(id)}`, send("PATCH", req));
export const runPreset = (id: string) =>
  apiFetch<EnqueueBuildResponse>(`/api/build-presets/${enc(id)}/run`, send("POST"));
