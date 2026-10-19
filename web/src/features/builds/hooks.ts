import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import * as api from "../../api/builds";
import { TopicBuildPrefix, TopicBuilds, type WSMessage } from "../../api/gen/api.gen";
import { useWS } from "../../api/wsContext";

export const buildKeys = {
  all: ["builds"] as const,
  list: (f: api.BuildFilters) => ["builds", "list", f.project ?? "", f.status ?? ""] as const,
  one: (id: string) => ["builds", "one", id] as const,
  presets: ["build-presets"] as const,
};

export const useBuilds = (f: api.BuildFilters) =>
  useQuery({ queryKey: buildKeys.list(f), queryFn: () => api.fetchBuilds(f) });
export const useBuild = (id: string) => useQuery({ queryKey: buildKeys.one(id), queryFn: () => api.fetchBuild(id) });
export const usePresets = () => useQuery({ queryKey: buildKeys.presets, queryFn: () => api.fetchPresets() });

/** Refetches builds when the "builds" topic (or "build:<id>" when given) reports a change. */
export function useBuildsLive(id?: string) {
  const ws = useWS();
  const qc = useQueryClient();
  useEffect(() => {
    const refetch = (msg?: WSMessage) => {
      // Log lines have their own consumer.
      if (msg && typeof msg.data === "object" && msg.data !== null && "line" in msg.data) return;
      void qc.invalidateQueries({ queryKey: buildKeys.all });
    };
    return ws.subscribe(id ? TopicBuildPrefix + id : TopicBuilds, {
      onEvent: refetch,
      onGap: () => {
        refetch();
      },
    });
  }, [ws, qc, id]);
}

/** A mutation that refreshes the build queries (or `keys`) on success. */
export function useBuildMutation<V, R>(fn: (v: V) => Promise<R>, keys: readonly unknown[] = buildKeys.all) {
  const qc = useQueryClient();
  return useMutation({ mutationFn: fn, onSuccess: () => qc.invalidateQueries({ queryKey: keys }) });
}
