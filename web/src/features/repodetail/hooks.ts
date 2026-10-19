import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { TopicHosts, TopicRepoPrefix, type RepoStatusDTO } from "../../api/gen/api.gen";
import { fetchRepoHost } from "../../api/hosts";
import * as api from "../../api/repodetail";
import { fetchRepos } from "../../api/repos";
import { useWS } from "../../api/wsContext";
import { patchRepo, repoKeys } from "../repos/hooks";

export const detailKeys = {
  changes: (id: string) => ["repo", id, "changes"] as const,
  diff: (id: string, path: string, mode: string) => ["repo", id, "diff", path, mode] as const,
  log: (id: string) => ["repo", id, "log"] as const,
  commit: (id: string, oid: string) => ["repo", id, "commit", oid] as const,
  branches: (id: string) => ["repo", id, "branches"] as const,
  host: (id: string) => ["repo", id, "host"] as const,
};

/** The repo's row and its project's name, from the same list query as the projects page. */
export function useRepoStatus(id: string) {
  return useQuery({
    queryKey: repoKeys.list("all"),
    queryFn: () => fetchRepos("all"),
    select: (d) => {
      for (const p of d.projects) {
        const repo = p.repos.find((r) => r.id === id);
        if (repo) return { repo, projectName: p.name };
      }
      return null;
    },
  });
}

export const useChanges = (id: string) => useQuery({ queryKey: detailKeys.changes(id), queryFn: () => api.fetchChanges(id) });
export const useDiff = (id: string, path: string | null, mode: api.DiffMode) =>
  useQuery({
    queryKey: detailKeys.diff(id, path ?? "", mode),
    queryFn: () => api.fetchDiff(id, path ?? "", mode),
    enabled: path !== null,
  });
export const useLog = (id: string, limit: number) =>
  useQuery({ queryKey: [...detailKeys.log(id), limit], queryFn: () => api.fetchLog(id, limit) });
export const useCommit = (id: string, oid: string | null) =>
  useQuery({ queryKey: detailKeys.commit(id, oid ?? ""), queryFn: () => api.fetchCommit(id, oid ?? ""), enabled: oid !== null });
export const useBranches = (id: string) => useQuery({ queryKey: detailKeys.branches(id), queryFn: () => api.fetchBranches(id) });
export const useRepoHost = (id: string) => useQuery({ queryKey: detailKeys.host(id), queryFn: () => fetchRepoHost(id) });

function isRepoStatus(v: unknown): v is RepoStatusDTO {
  return typeof v === "object" && v !== null && typeof (v as { id?: unknown }).id === "string";
}

/** Live updates: the repo row, host data and working-tree reads. */
export function useRepoDetailLive(id: string) {
  const ws = useWS();
  const qc = useQueryClient();
  useEffect(() => {
    const refetchTree = () => {
      for (const k of ["changes", "diff", "log", "commit", "branches"])
        void qc.invalidateQueries({ queryKey: ["repo", id, k] });
    };
    const offRepo = ws.subscribe(TopicRepoPrefix + id, {
      onEvent: (msg) => {
        if (isRepoStatus(msg.data)) {
          const next = msg.data;
          qc.setQueriesData<Parameters<typeof patchRepo>[0]>({ queryKey: ["repos", "list"] }, (old) => patchRepo(old, next));
        }
        refetchTree();
      },
      onGap: () => {
        void qc.invalidateQueries({ queryKey: repoKeys.all });
        refetchTree();
      },
    });
    const refetchHost = () => {
      void qc.invalidateQueries({ queryKey: detailKeys.host(id) });
    };
    const offHosts = ws.subscribe(TopicHosts, { onEvent: refetchHost, onGap: refetchHost });
    return () => {
      offRepo();
      offHosts();
    };
  }, [ws, qc, id]);
}
