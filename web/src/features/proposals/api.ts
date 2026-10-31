import { apiFetch } from "../../api/client";
import type { ConfirmProposalRequest, ProposalDTO, ProposalsResponse } from "../../api/gen/api.gen";

const enc = encodeURIComponent;
const post = (body?: unknown): RequestInit => ({ method: "POST", ...(body === undefined ? {} : { body: JSON.stringify(body) }) });

export const fetchTaskProposals = async (taskId: string): Promise<ProposalDTO[]> =>
  (await apiFetch<ProposalsResponse>(`/api/proposals?task=${enc(taskId)}`)).proposals;

export const confirmProposal = (id: string, hash: string) =>
  apiFetch<ProposalDTO>(`/api/proposals/${enc(id)}/confirm`, post({ hash } satisfies ConfirmProposalRequest));
export const rejectProposal = (id: string) => apiFetch<ProposalDTO>(`/api/proposals/${enc(id)}/reject`, post());
export const retryProposal = (id: string) => apiFetch<ProposalDTO>(`/api/proposals/${enc(id)}/retry`, post());
