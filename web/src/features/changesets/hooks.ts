import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useContext, useEffect } from "react";
import { TopicChangeSets, TopicProposals, type ScopeDTO } from "../../api/gen/api.gen";
import { WSContext } from "../../api/wsContext";
import { proposalKeys } from "../proposals";
import * as api from "./api";

export const csKeys = {
  all: ["changesets"] as const,
  task: (taskId: string) => ["changesets", "task", taskId] as const,
  worktrees: (taskId: string) => ["changesets", "worktrees", taskId] as const,
};

const noRetry = (count: number, error: unknown) => !api.isNotReady(error) && count < 1;

function useLive() {
  const ws = useContext(WSContext);
  const qc = useQueryClient();
  useEffect(() => {
    if (!ws) return;
    const refetch = () => {
      void qc.invalidateQueries({ queryKey: csKeys.all });
    };
    const offs = [TopicChangeSets, TopicProposals].map((t) => ws.subscribe(t, { onEvent: refetch, onGap: refetch }));
    return () => {
      offs.forEach((off) => {
        off();
      });
    };
  }, [ws, qc]);
}

export function useTaskChangeSet(taskId: string) {
  useLive();
  return useQuery({ queryKey: csKeys.task(taskId), queryFn: () => api.fetchTaskChangeSet(taskId), retry: noRetry });
}

export function useWorktrees(taskId: string) {
  return useQuery({ queryKey: csKeys.worktrees(taskId), queryFn: () => api.fetchWorktrees(taskId), retry: noRetry });
}

export function useChangeSetMutations(changeSetId: string) {
  const qc = useQueryClient();
  const done = {
    onSettled: () => Promise.all([qc.invalidateQueries({ queryKey: csKeys.all }), qc.invalidateQueries({ queryKey: proposalKeys.all })]),
  };
  return {
    setOrder: useMutation({ mutationFn: (order: string[]) => api.setOrder(changeSetId, order), ...done }),
    preflight: useMutation({ mutationFn: (scope: ScopeDTO) => api.preflight(changeSetId, scope) }),
    confirm: useMutation({ mutationFn: (req: Parameters<typeof api.confirmRun>[1]) => api.confirmRun(changeSetId, req), ...done }),
    stop: useMutation({ mutationFn: () => api.stopRun(changeSetId), ...done }),
    close: useMutation({ mutationFn: () => api.closeChangeSet(changeSetId), ...done }),
  };
}

export function useLandingMutations() {
  const qc = useQueryClient();
  const done = {
    onSettled: () => Promise.all([qc.invalidateQueries({ queryKey: csKeys.all }), qc.invalidateQueries({ queryKey: proposalKeys.all })]),
  };
  return {
    mergeLocal: useMutation({ mutationFn: (v: { worktreeId: string; title?: string }) => api.proposeMergeLocal(v.worktreeId, v.title), ...done }),
    remove: useMutation({ mutationFn: (worktreeId: string) => api.proposeRemoveWorktree(worktreeId), ...done }),
  };
}
