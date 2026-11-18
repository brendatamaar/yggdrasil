import { apiFetch } from "../../api/client";
import type { FailedJobsResponse, IssueResponse, IssuesResponse, MRContextResponse } from "../../api/gen/api.gen";

// Typed client of RouteFailedJobs, RouteIssues, RouteIssue and RouteMRContext. Bodies are
// checked for the status the views depend on; a malformed answer is an error, not an empty view.

const enc = encodeURIComponent;

export class HostContextDecodeError extends Error {
  constructor() {
    super("The server sent an unexpected answer. Try again.");
    this.name = "HostContextDecodeError";
  }
}

const hasStatus = (v: unknown): boolean => {
  if (typeof v !== "object" || v === null) return false;
  const s = (v as { status?: unknown }).status;
  return typeof s === "object" && s !== null && typeof (s as { phase?: unknown }).phase === "string";
};

async function get<T>(path: string, listKey?: string): Promise<T> {
  const v = await apiFetch<unknown>(path);
  if (!hasStatus(v)) throw new HostContextDecodeError();
  if (listKey && !Array.isArray((v as Record<string, unknown>)[listKey])) throw new HostContextDecodeError();
  return v as T;
}

export const fetchFailedJobs = (repoId: string, runId: string) =>
  get<FailedJobsResponse>(`/api/repos/${enc(repoId)}/ci/runs/${enc(runId)}/failed-jobs`, "jobs");
export const fetchIssues = (repoId: string) => get<IssuesResponse>(`/api/repos/${enc(repoId)}/issues`, "issues");
export const fetchIssue = (repoId: string, number: number) => get<IssueResponse>(`/api/repos/${enc(repoId)}/issues/${String(number)}`);
export const fetchMRContext = (repoId: string, number: number) =>
  get<MRContextResponse>(`/api/repos/${enc(repoId)}/mrs/${String(number)}/context`);
