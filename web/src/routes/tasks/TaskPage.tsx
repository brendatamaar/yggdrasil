import { useMutation, useQueryClient } from "@tanstack/react-query";
import { describeError } from "../../api/client";
import { cancelTask, taskKeys, useTask } from "../../api/queries";
import { Transcript, UsageSummary } from "../../components/transcript/Transcript";
import { taskTotals } from "../../components/transcript/tree";
import { useTranscript } from "../../components/transcript/useTranscript";
import { statusLabel } from "../../stores/notifications";

const cancellable = new Set(["queued", "running"]);

/** One task: its status, its totals and the live transcript. */
export function TaskPage({ taskId }: { taskId: string }) {
  const task = useTask(taskId);
  const transcript = useTranscript(taskId);
  const queryClient = useQueryClient();
  const cancel = useMutation({
    mutationFn: () => cancelTask(taskId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: taskKeys.all }),
  });

  const status = task.data?.status;
  const loadError = task.error ? describeError(task.error) : transcript.error;

  return (
    <section aria-labelledby="page-title" className="mx-auto max-w-3xl space-y-4">
      <header className="space-y-1">
        <h1 id="page-title" className="text-lg font-semibold">
          {task.data?.title || "Task"}
        </h1>
        <div className="flex items-center gap-3 text-sm">
          {status ? (
            <span className="rounded-full border border-line px-2 py-0.5" data-testid="task-status">
              {statusLabel(status)}
            </span>
          ) : null}
          {status && cancellable.has(status) ? (
            <button
              type="button"
              className="rounded border border-line px-2 py-0.5 hover:bg-surface"
              disabled={cancel.isPending}
              onClick={() => {
                cancel.mutate();
              }}
            >
              Cancel task
            </button>
          ) : null}
        </div>
        <UsageSummary {...taskTotals(transcript.events)} />
      </header>

      {loadError ? (
        <p role="alert" className="text-sm text-danger">
          {loadError}
        </p>
      ) : null}
      {cancel.error ? (
        <p role="alert" className="text-sm text-danger">
          {describeError(cancel.error)}
        </p>
      ) : null}
      {transcript.loaded ? (
        <Transcript events={transcript.events} />
      ) : (
        <p className="text-sm text-muted">Loading the transcript…</p>
      )}
    </section>
  );
}
