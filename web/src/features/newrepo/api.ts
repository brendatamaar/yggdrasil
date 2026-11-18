import { apiFetch } from "../../api/client";
import type {
  NewRepoOperationDTO,
  NewRepoPreviewRequest,
  NewRepoPreviewResponse,
  ProposalDTO,
  PublishNewRepoRequest,
  PublishPreviewRequest,
  PublishPreviewResponse,
  ReserveNewRepoRequest,
  RetryNewRepoRequest,
} from "../../api/gen/api.gen";

const enc = encodeURIComponent;
const post = (body: unknown, signal?: AbortSignal): RequestInit => ({ method: "POST", body: JSON.stringify(body), ...(signal ? { signal } : {}) });

const STATES = new Set(["reserved", "awaiting_confirm", "initializing", "ready", "publishing", "published", "uncertain", "stopped"]);

export class OperationDecodeError extends Error {
  constructor() {
    super("Yggdrasil sent a new-repo state this page does not understand. Reload the page.");
    this.name = "OperationDecodeError";
  }
}

/** Accepts only a known lifecycle variant; the page never guesses a stage from other fields. */
export function parseOperation(raw: unknown): NewRepoOperationDTO {
  if (typeof raw !== "object" || raw === null) throw new OperationDecodeError();
  const o = raw as Record<string, unknown>;
  if (typeof o.operationId !== "string" || typeof o.leaf !== "string" || typeof o.state !== "string" || !STATES.has(o.state)) {
    throw new OperationDecodeError();
  }
  if (o.stages !== undefined && !Array.isArray(o.stages)) throw new OperationDecodeError();
  return raw as NewRepoOperationDTO;
}

export const previewNewRepo = (projectId: string, req: NewRepoPreviewRequest, signal?: AbortSignal) =>
  apiFetch<NewRepoPreviewResponse>(`/api/projects/${enc(projectId)}/new-repos/preview`, post(req, signal));

export const reserveNewRepo = async (projectId: string, req: ReserveNewRepoRequest) =>
  parseOperation(await apiFetch<unknown>(`/api/projects/${enc(projectId)}/new-repos`, post(req)));

export const getNewRepo = async (id: string, signal?: AbortSignal) =>
  parseOperation(await apiFetch<unknown>(`/api/new-repos/${enc(id)}`, signal ? { signal } : undefined));

export const listNewRepos = async (projectId: string, signal?: AbortSignal): Promise<NewRepoOperationDTO[]> => {
  const raw = await apiFetch<unknown>(`/api/projects/${enc(projectId)}/new-repos`, signal ? { signal } : undefined);
  if (!Array.isArray(raw)) throw new OperationDecodeError();
  return raw.map(parseOperation);
};

export const previewPublish = (id: string, req: PublishPreviewRequest, signal?: AbortSignal) =>
  apiFetch<PublishPreviewResponse>(`/api/new-repos/${enc(id)}/publish/preview`, post(req, signal));

/** Creates the pending publish proposal. The remote is made only after it is confirmed through the proposals UI. */
export const publishNewRepo = (id: string, req: PublishNewRepoRequest) => apiFetch<ProposalDTO>(`/api/new-repos/${enc(id)}/publish`, post(req));

export const retryNewRepo = async (id: string, hash: string) =>
  parseOperation(await apiFetch<unknown>(`/api/new-repos/${enc(id)}/retry`, post({ hash } satisfies RetryNewRepoRequest)));
