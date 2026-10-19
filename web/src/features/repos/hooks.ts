import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { TopicRepoPrefix, TopicRepos, type RepoStatusDTO, type ReposResponse } from "../../api/gen/api.gen";
import * as api from "../../api/repos";
import type { RepoFilter } from "../../api/repos";
import { useWS } from "../../api/wsContext";

export const repoKeys = {
  all: ["repos"] as const,
  list: (filter: RepoFilter) => ["repos", "list", filter] as const,
  dirs: (path: string) => ["fs", "dirs", path] as const,
};

export function useRepos(filter: RepoFilter) {
  return useQuery({ queryKey: repoKeys.list(filter), queryFn: () => api.fetchRepos(filter) });
}

export function useDirs(path: string) {
  return useQuery({ queryKey: repoKeys.dirs(path), queryFn: () => api.fetchDirs(path) });
}

/** A mutation that refreshes the repo lists when it succeeds. */
export function useRepoMutation<V, R>(fn: (v: V) => Promise<R>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => qc.invalidateQueries({ queryKey: repoKeys.all }),
  });
}

function isRepoStatus(v: unknown): v is RepoStatusDTO {
  return typeof v === "object" && v !== null && typeof (v as { id?: unknown }).id === "string";
}

/** Replaces the row of `next` in a cached list. */
export function patchRepo(data: ReposResponse | undefined, next: RepoStatusDTO): ReposResponse | undefined {
  if (!data) return data;
  return {
    ...data,
    projects: data.projects.map((p) => ({
      ...p,
      repos: p.repos.map((r) => (r.id === next.id ? next : r)),
    })),
  };
}

/** "repos" refetches the lists; "repo:<id>" updates that row from the message. */
export function useReposLive(visibleIds: readonly string[]) {
  const ws = useWS();
  const qc = useQueryClient();
  const key = visibleIds.join(",");

  useEffect(() => {
    const refetch = () => {
      void qc.invalidateQueries({ queryKey: repoKeys.all });
    };
    return ws.subscribe(TopicRepos, { onEvent: refetch, onGap: refetch });
  }, [ws, qc]);

  useEffect(() => {
    const unsubs = (key ? key.split(",") : []).map((id) =>
      ws.subscribe(TopicRepoPrefix + id, {
        onEvent: (msg) => {
          if (isRepoStatus(msg.data)) {
            const next = msg.data;
            qc.setQueriesData<ReposResponse>({ queryKey: ["repos", "list"] }, (old) => patchRepo(old, next));
          }
        },
        onGap: () => {
          void qc.invalidateQueries({ queryKey: repoKeys.all });
        },
      }),
    );
    return () => {
      unsubs.forEach((u) => {
        u();
      });
    };
  }, [ws, qc, key]);
}

const FOCUS_THROTTLE_MS = 15_000;

/** Asks for a refresh when the window regains focus, at most once per 15 s. */
export function useRefreshOnFocus() {
  const last = useRef(0);
  useEffect(() => {
    const onFocus = () => {
      const now = Date.now();
      if (now - last.current < FOCUS_THROTTLE_MS) return;
      last.current = now;
      void api.refreshRepos({ ids: [], reason: "focus" }).catch(() => undefined);
    };
    window.addEventListener("focus", onFocus);
    return () => {
      window.removeEventListener("focus", onFocus);
    };
  }, []);
}
