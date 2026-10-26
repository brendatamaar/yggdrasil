import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useContext, useEffect } from "react";
import { TopicBoard } from "../../api/gen/api.gen";
import { taskKeys } from "../../api/queries";
import { WSContext } from "../../api/wsContext";
import { cancelTask, fetchBoard } from "./api";

export const boardKeys = { all: ["board"] as const };

export const useBoard = () => useQuery({ queryKey: boardKeys.all, queryFn: fetchBoard });

/**
 * Refetches the board when the "board" topic reports a change. Without a
 * WebSocket provider (isolated widget tests) it does nothing.
 */
export function useBoardLive() {
  const ws = useContext(WSContext);
  const qc = useQueryClient();
  useEffect(() => {
    if (!ws) return;
    const refetch = () => {
      void qc.invalidateQueries({ queryKey: boardKeys.all });
    };
    return ws.subscribe(TopicBoard, { onEvent: refetch, onGap: refetch });
  }, [ws, qc]);
}

export function useCancelTask() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => cancelTask(id),
    onSuccess: () =>
      Promise.all([qc.invalidateQueries({ queryKey: boardKeys.all }), qc.invalidateQueries({ queryKey: taskKeys.all })]),
  });
}
