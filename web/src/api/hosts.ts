import { apiFetch } from "./client";
import type { RepoHostResponse } from "./gen/api.gen";

export const fetchRepoHost = (id: string) =>
  apiFetch<RepoHostResponse>(`/api/repos/${encodeURIComponent(id)}/host`);
