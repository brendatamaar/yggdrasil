import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import {
  copyProfile,
  fetchRepoEnv,
  fetchWorktreeEnv,
  runComposeAction,
  saveRepoEnv,
  type ComposeAction,
} from "../../api/repoenv";
import { TopicWorktreeEnv, type RepoEnvDTO, type WorktreeEnvDTO } from "../../api/gen/api.gen";
import { useWS } from "../../api/wsContext";

export const envKeys = {
  repo: (id: string) => ["repoenv", id] as const,
  worktree: (id: string) => ["worktreeenv", id] as const,
};

export const useRepoEnv = (repoId: string) =>
  useQuery({ queryKey: envKeys.repo(repoId), queryFn: () => fetchRepoEnv(repoId) });

export function useSaveRepoEnv() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (env: RepoEnvDTO) => saveRepoEnv(env),
    onSuccess: (env) => {
      qc.setQueryData(envKeys.repo(env.repoId), env);
    },
  });
}

function parseEnvChanged(data: unknown): WorktreeEnvDTO | null {
  if (typeof data !== "object" || data === null) return null;
  const env = (data as { env?: unknown }).env;
  if (typeof env !== "object" || env === null) return null;
  return typeof (env as { worktreeId?: unknown }).worktreeId === "string" ? (env as WorktreeEnvDTO) : null; // worktreeId checked
}

/** One worktree's environment, kept fresh by the "worktreeenv" topic. */
export function useWorktreeEnv(worktreeId: string) {
  const ws = useWS();
  const qc = useQueryClient();
  useEffect(
    () =>
      ws.subscribe(TopicWorktreeEnv, {
        onEvent: (msg) => {
          const env = parseEnvChanged(msg.data);
          if (env?.worktreeId === worktreeId) qc.setQueryData(envKeys.worktree(worktreeId), env);
        },
        onGap: () => {
          void qc.invalidateQueries({ queryKey: envKeys.worktree(worktreeId) });
        },
      }),
    [ws, qc, worktreeId],
  );
  return useQuery({ queryKey: envKeys.worktree(worktreeId), queryFn: () => fetchWorktreeEnv(worktreeId) });
}

export function useCopyProfile(worktreeId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => copyProfile(worktreeId),
    onSuccess: (env) => {
      qc.setQueryData(envKeys.worktree(worktreeId), env);
    },
  });
}

export function useComposeAction(worktreeId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (action: ComposeAction) => runComposeAction(worktreeId, action),
    onSuccess: (env) => {
      qc.setQueryData(envKeys.worktree(worktreeId), env);
    },
  });
}
