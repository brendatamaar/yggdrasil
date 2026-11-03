import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useContext, useEffect } from "react";
import { TopicChangeSets, TopicVerifications } from "../../api/gen/api.gen";
import { WSContext } from "../../api/wsContext";
import * as api from "./api";

export const reviewKeys = {
  all: ["review"] as const,
  verifications: (taskId: string) => ["review", "verifications", taskId] as const,
  notes: (taskId: string) => ["review", "notes", taskId] as const,
  diff: (taskId: string, repoId: string) => ["review", "diff", taskId, repoId] as const,
};

const noRetry = (count: number, error: unknown) => !api.isNotReady(error) && count < 1;

/** Refetches review data when the verifications or changesets topic reports a change. */
function useLiveReview() {
  const ws = useContext(WSContext);
  const qc = useQueryClient();
  useEffect(() => {
    if (!ws) return;
    const refetch = () => {
      void qc.invalidateQueries({ queryKey: reviewKeys.all });
    };
    const offs = [TopicVerifications, TopicChangeSets].map((t) => ws.subscribe(t, { onEvent: refetch, onGap: refetch }));
    return () => {
      offs.forEach((off) => {
        off();
      });
    };
  }, [ws, qc]);
}

export function useVerifications(taskId: string) {
  useLiveReview();
  return useQuery({ queryKey: reviewKeys.verifications(taskId), queryFn: () => api.fetchVerifications(taskId), retry: noRetry });
}

export function useNotes(taskId: string) {
  return useQuery({ queryKey: reviewKeys.notes(taskId), queryFn: () => api.fetchNotes(taskId), retry: noRetry });
}

export function useRepoDiff(taskId: string, repoId: string, load: api.DiffLoader) {
  return useQuery({ queryKey: reviewKeys.diff(taskId, repoId), queryFn: () => load(taskId, repoId), retry: noRetry });
}

export function useReviewMutations(taskId: string) {
  const qc = useQueryClient();
  const done = { onSettled: () => qc.invalidateQueries({ queryKey: reviewKeys.all }) };
  return {
    retry: useMutation({ mutationFn: () => api.retryVerification(taskId), ...done }),
    cancel: useMutation({ mutationFn: (id: string) => api.cancelVerification(id), ...done }),
    sendFailure: useMutation({ mutationFn: (v: { id: string; optIn: boolean }) => api.sendFailure(v.id, v.optIn), ...done }),
    sendNotes: useMutation({ mutationFn: (v: { ids: string[]; optIn: boolean }) => api.sendNotes(taskId, v.ids, v.optIn), ...done }),
    deleteNote: useMutation({ mutationFn: (id: string) => api.deleteNote(id), ...done }),
    saveNote: useMutation({
      mutationFn: (v: { noteId?: string; text: string; create?: Parameters<typeof api.createNote>[1] }) =>
        v.noteId ? api.editNote(v.noteId, v.text) : v.create ? api.createNote(taskId, v.create) : Promise.reject(new Error("no anchor")),
      ...done,
    }),
  };
}
