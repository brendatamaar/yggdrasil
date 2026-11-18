import { useQuery } from "@tanstack/react-query";
import * as api from "./api";

// Host reads are on demand (never polled): each hook is enabled only when its view is open.

export const hostContextKeys = {
  failedJobs: (repoId: string, runId: string) => ["hostcontext", "failed-jobs", repoId, runId] as const,
  issues: (repoId: string) => ["hostcontext", "issues", repoId] as const,
  issue: (repoId: string, n: number) => ["hostcontext", "issue", repoId, n] as const,
  mr: (repoId: string, n: number) => ["hostcontext", "mr", repoId, n] as const,
};

export const useFailedJobs = (repoId: string, runId: string) =>
  useQuery({ queryKey: hostContextKeys.failedJobs(repoId, runId), queryFn: () => api.fetchFailedJobs(repoId, runId) });
export const useIssues = (repoId: string, enabled = true) =>
  useQuery({ queryKey: hostContextKeys.issues(repoId), queryFn: () => api.fetchIssues(repoId), enabled });
export const useIssue = (repoId: string, n: number) =>
  useQuery({ queryKey: hostContextKeys.issue(repoId, n), queryFn: () => api.fetchIssue(repoId, n) });
export const useMRContext = (repoId: string, n: number) =>
  useQuery({ queryKey: hostContextKeys.mr(repoId, n), queryFn: () => api.fetchMRContext(repoId, n) });
