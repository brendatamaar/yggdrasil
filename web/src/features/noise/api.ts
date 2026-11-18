import { apiFetch } from "../../api/client";
import type { CreateIgnoreProposalRequest, NoiseResponse, ProposalDTO } from "../../api/gen/api.gen";

// Typed client of RouteNoise and RouteNoiseProposal (WS-09). Nothing here writes a file: the
// proposal is pending until the person confirms it through the proposal review.

const enc = encodeURIComponent;

export class NoiseDecodeError extends Error {
  constructor() {
    super("The server sent an unexpected answer. Try again.");
    this.name = "NoiseDecodeError";
  }
}

export async function fetchNoise(repoId: string): Promise<NoiseResponse> {
  const v = await apiFetch<unknown>(`/api/repos/${enc(repoId)}/noise`);
  const o = v as Partial<NoiseResponse> | null;
  if (typeof o !== "object" || o === null || typeof o.available !== "boolean" || !Array.isArray(o.candidates)) {
    throw new NoiseDecodeError();
  }
  return o as NoiseResponse;
}

export const createIgnoreProposal = (repoId: string, req: CreateIgnoreProposalRequest) =>
  apiFetch<ProposalDTO>(`/api/repos/${enc(repoId)}/noise/proposals`, { method: "POST", body: JSON.stringify(req) });
