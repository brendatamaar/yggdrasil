import { apiFetch } from "../../api/client";
import type { ActionResultDTO, StandupPolishRequest, StandupResponse } from "../../api/gen/api.gen";

// Typed client of RouteStandup and RouteStandupPolish. The summary is read-only; polish is the
// one explicit submission and is never called from a refresh.

const enc = encodeURIComponent;
const PHASES = new Set(["ready", "partial", "failed"]);

export class StandupDecodeError extends Error {
  constructor() {
    super("The server sent an unexpected standup. Try again.");
    this.name = "StandupDecodeError";
  }
}

export function parseStandup(v: unknown): StandupResponse {
  const o = v as Partial<StandupResponse> | null;
  if (
    typeof o !== "object" ||
    o === null ||
    typeof o.phase !== "string" ||
    !PHASES.has(o.phase) ||
    typeof o.text !== "string" ||
    typeof o.from !== "string" ||
    typeof o.to !== "string" ||
    !Array.isArray(o.commits) ||
    !Array.isArray(o.mrs) ||
    !Array.isArray(o.builds)
  ) {
    throw new StandupDecodeError();
  }
  return o as StandupResponse;
}

export const fetchStandup = async (projectId: string, from: string, to: string, signal?: AbortSignal): Promise<StandupResponse> =>
  parseStandup(
    await apiFetch<unknown>(`/api/projects/${enc(projectId)}/standup?from=${enc(from)}&to=${enc(to)}`, signal ? { signal } : undefined),
  );

export const polishStandup = (projectId: string, req: StandupPolishRequest) =>
  apiFetch<ActionResultDTO>(`/api/projects/${enc(projectId)}/standup/polish`, { method: "POST", body: JSON.stringify(req) });
