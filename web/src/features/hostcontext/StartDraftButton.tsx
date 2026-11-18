import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch, describeError } from "../../api/client";
import type { StartDraftRequest, TaskResponse } from "../../api/gen/api.gen";
import { hostContextKeys } from "./hooks";

/**
 * The explicit second step of an imported issue (AC-05): moves one draft into the ordinary
 * queue as an Ask task. Importing never starts anything; this button is the only way here.
 */
export function StartDraftButton({ taskId, repoId }: { taskId: string; repoId: string }) {
  const qc = useQueryClient();
  const m = useMutation({
    mutationFn: () =>
      apiFetch<TaskResponse>(`/api/tasks/${encodeURIComponent(taskId)}/start`, {
        method: "POST",
        body: JSON.stringify({ mode: "ask" } satisfies StartDraftRequest),
      }),
    onSettled: () => Promise.all([qc.invalidateQueries({ queryKey: ["tasks"] }), qc.invalidateQueries({ queryKey: hostContextKeys.issues(repoId) })]),
  });
  return (
    <span className="inline-flex items-center gap-1">
      <button
        type="button"
        disabled={m.isPending || m.isSuccess}
        className="rounded border border-line px-2 py-0.5 text-xs hover:border-accent disabled:opacity-50"
        onClick={() => {
          m.mutate();
        }}
      >
        {m.isSuccess ? "Started" : "Start as Ask task"}
      </button>
      {m.isError ? (
        <span role="alert" className="text-xs text-danger">
          {describeError(m.error)}
        </span>
      ) : null}
    </span>
  );
}
