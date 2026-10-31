import { apiFetch } from "../../api/client";
import type { TrustDTO, TrustRequest } from "../../api/gen/api.gen";

const path = (repoId: string) => `/api/repos/${encodeURIComponent(repoId)}/trust`;

export const fetchTrust = (repoId: string) => apiFetch<TrustDTO>(path(repoId));
export const setTrust = (repoId: string, hash: string) =>
  apiFetch<TrustDTO>(path(repoId), { method: "POST", body: JSON.stringify({ hash } satisfies TrustRequest) });
export const revokeTrust = (repoId: string) => apiFetch<TrustDTO>(path(repoId), { method: "DELETE" });
