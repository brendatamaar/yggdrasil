import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { fetchRepos } from "../../api/repos";
import * as api from "./api";

export const composerKeys = {
  prompts: ["composer", "prompts"] as const,
  models: (agent: string) => ["composer", "models", agent] as const,
  mentions: (project: string, q: string) => ["composer", "mentions", project, q] as const,
  repos: ["composer", "repos"] as const,
};

/** Tracked projects and their repos. */
export function useComposerRepos() {
  return useQuery({ queryKey: composerKeys.repos, queryFn: async () => (await fetchRepos("all")).projects });
}

export function useModels(agent: string) {
  return useQuery({
    queryKey: composerKeys.models(agent),
    queryFn: async () => (await api.fetchModels(agent)).models,
    enabled: agent !== "",
  });
}

export function useSavedPrompts() {
  return useQuery({ queryKey: composerKeys.prompts, queryFn: api.fetchPrompts });
}

export function usePromptMutations() {
  const qc = useQueryClient();
  const done = { onSuccess: () => qc.invalidateQueries({ queryKey: composerKeys.prompts }) };
  return {
    save: useMutation({ mutationFn: api.savePrompt, ...done }),
    remove: useMutation({ mutationFn: api.deletePrompt, ...done }),
    // The server has no rename call: save under the new name, then delete the old one.
    rename: useMutation({
      mutationFn: async (v: { id: string; name: string; text: string }) => {
        await api.savePrompt({ name: v.name, text: v.text });
        await api.deletePrompt(v.id);
      },
      ...done,
    }),
  };
}

export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => {
      setV(value);
    }, ms);
    return () => {
      clearTimeout(t);
    };
  }, [value, ms]);
  return v;
}

/** Mentions for `q`, 150 ms after typing stops; a stale request is aborted by TanStack Query. */
export function useMentions(projectId: string, q: string, enabled: boolean) {
  const debounced = useDebounced(q, 150);
  return useQuery({
    queryKey: composerKeys.mentions(projectId, debounced),
    queryFn: ({ signal }) => api.fetchMentions(projectId, debounced, signal),
    enabled: enabled && projectId !== "",
    staleTime: 10_000,
    placeholderData: keepPreviousData,
  });
}
