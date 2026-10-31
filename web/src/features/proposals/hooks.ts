import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useContext, useEffect } from "react";
import { TopicProposals } from "../../api/gen/api.gen";
import { WSContext } from "../../api/wsContext";
import * as api from "./api";

export const proposalKeys = {
  all: ["proposals"] as const,
  task: (taskId: string) => ["proposals", "task", taskId] as const,
};

/** Proposals of one task, refetched when the "proposals" topic reports a change. */
export function useTaskProposals(taskId: string) {
  const ws = useContext(WSContext);
  const qc = useQueryClient();
  useEffect(() => {
    if (!ws) return;
    const refetch = () => {
      void qc.invalidateQueries({ queryKey: proposalKeys.all });
    };
    return ws.subscribe(TopicProposals, { onEvent: refetch, onGap: refetch });
  }, [ws, qc]);
  return useQuery({ queryKey: proposalKeys.task(taskId), queryFn: () => api.fetchTaskProposals(taskId) });
}

export function useProposalMutations() {
  const qc = useQueryClient();
  const done = { onSettled: () => qc.invalidateQueries({ queryKey: proposalKeys.all }) };
  return {
    confirm: useMutation({ mutationFn: (v: { id: string; hash: string }) => api.confirmProposal(v.id, v.hash), ...done }),
    reject: useMutation({ mutationFn: (id: string) => api.rejectProposal(id), ...done }),
    retry: useMutation({ mutationFn: (id: string) => api.retryProposal(id), ...done }),
  };
}
