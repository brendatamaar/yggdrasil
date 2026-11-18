import { apiFetch } from "../../api/client";
import type {
  ActionCatalogueResponse,
  ActionExecuteRequest,
  ActionPreviewDTO,
  ActionRequestDTO,
  ActionResultDTO,
} from "../../api/gen/api.gen";

// Typed client of the workspace-action endpoints (RouteRepoActions, RouteActionPreview,
// RouteActionExecute, RouteActionGet in internal/api/phase6.go). Every answer is checked at
// the boundary: a malformed body is an error, never an optimistic result.

const enc = encodeURIComponent;

export class ActionDecodeError extends Error {
  constructor(what: string) {
    super(`The server sent an unexpected ${what}. Try again.`);
    this.name = "ActionDecodeError";
  }
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null;
const isStr = (v: unknown): v is string => typeof v === "string";

const CONTEXT = new Set(["available", "metadata_only", "none"]);
const STATES = new Set(["submitting", "completed", "refused", "cancelled"]);

export function parseCatalogue(v: unknown): ActionCatalogueResponse {
  if (!isObj(v) || !isStr(v.repoId) || !Array.isArray(v.offerings)) throw new ActionDecodeError("action list");
  for (const o of v.offerings as unknown[]) {
    if (!isObj(o) || !isStr(o.kind) || !isStr(o.reason) || !isStr(o.title) || !isStr(o.context) || !CONTEXT.has(o.context)) {
      throw new ActionDecodeError("action list");
    }
  }
  return v as unknown as ActionCatalogueResponse;
}

export function parsePreview(v: unknown): ActionPreviewDTO {
  if (
    !isObj(v) ||
    !isStr(v.requestId) ||
    !isStr(v.digest) ||
    v.digest === "" ||
    !isStr(v.kind) ||
    !isStr(v.source) ||
    !isStr(v.contextText) ||
    !isStr(v.context) ||
    !CONTEXT.has(v.context)
  ) {
    throw new ActionDecodeError("preview");
  }
  return v as unknown as ActionPreviewDTO;
}

export function parseResult(v: unknown): ActionResultDTO {
  if (!isObj(v) || !isStr(v.requestId) || !isStr(v.state) || !STATES.has(v.state) || !isStr(v.message)) {
    throw new ActionDecodeError("result");
  }
  return v as unknown as ActionResultDTO;
}

export const fetchRepoActions = async (repoId: string, signal?: AbortSignal): Promise<ActionCatalogueResponse> =>
  parseCatalogue(await apiFetch<unknown>(`/api/repos/${enc(repoId)}/actions`, signal ? { signal } : undefined));

export const previewAction = async (req: ActionRequestDTO, signal?: AbortSignal): Promise<ActionPreviewDTO> =>
  parsePreview(
    await apiFetch<unknown>("/api/actions/preview", { method: "POST", body: JSON.stringify(req), ...(signal ? { signal } : {}) }),
  );

/** Execute needs the digest from the preview; without it the call cannot be made (the type forbids it). */
export const executeAction = async (body: ActionExecuteRequest): Promise<ActionResultDTO> =>
  parseResult(await apiFetch<unknown>("/api/actions/execute", { method: "POST", body: JSON.stringify(body) }));

export const getAction = async (requestId: string): Promise<ActionResultDTO> =>
  parseResult(await apiFetch<unknown>(`/api/actions/${enc(requestId)}`));
