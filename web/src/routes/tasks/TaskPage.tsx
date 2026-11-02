import type { Capabilities } from "../../api/gen/agents.gen";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { describeError } from "../../api/client";
import { cancelTask, taskKeys, useTask } from "../../api/queries";
import { useBoard } from "../../features/board";
import { MessageComposer, type Mode } from "../../features/composer";
import { ChangeSetPanel } from "../../features/changesets";
import { ProposalsPanel } from "../../features/proposals";
import { ReviewPanel } from "../../features/review";
import { NeedsYouNotice, TrustPanel } from "../../features/trust";
import { StatusLine, Transcript, UsageSummary, taskUsage, useTranscript } from "../../features/transcript";
import { statusLabel } from "../../stores/notifications";

// The API does not expose the adapter capabilities yet; undefined shows every feature.
const capabilities: Capabilities | undefined = undefined;
const idleStatuses = new Set(["needs_you", "review", "done"]);
const cancellable = new Set(["queued", "running"]);

type Tab = "transcript" | "review" | "changeset";
const tabs: { id: Tab; label: string }[] = [
  { id: "transcript", label: "Transcript" },
  { id: "review", label: "Review" },
  { id: "changeset", label: "Change set" },
];

/** One task: its status, its totals and the live transcript. */
export function TaskPage({ taskId }: { taskId: string }) {
  const task = useTask(taskId);
  const transcript = useTranscript(taskId);
  const queryClient = useQueryClient();
  const cancel = useMutation({
    mutationFn: () => cancelTask(taskId),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: taskKeys.all }),
  });

  const [tab, setTab] = useState<Tab>("transcript");
  const board = useBoard();
  const reason = board.data?.cards.find((c) => c.taskId === taskId)?.needsYouReason;
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
        <UsageSummary usage={taskUsage(transcript.events)} capabilities={capabilities} />
        <StatusLine events={transcript.events} label={status ? statusLabel(status) : undefined} />
      </header>

      {status === "needs_you" ? <NeedsYouNotice reason={reason} /> : null}
      {task.data?.repoIds.map((id) => (
        <TrustPanel key={id} repoId={id} />
      ))}
      {task.data ? <ProposalsPanel taskId={taskId} /> : null}

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
      <div role="tablist" aria-label="Task views" className="flex gap-1 border-b border-line">
        {tabs.map((t, i) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            id={`tab-${t.id}`}
            aria-selected={tab === t.id}
            aria-controls={`panel-${t.id}`}
            tabIndex={tab === t.id ? 0 : -1}
            className={`px-3 py-1 text-sm ${tab === t.id ? "border-b-2 border-accent font-medium" : "text-muted"}`}
            onClick={() => {
              setTab(t.id);
            }}
            onKeyDown={(e) => {
              if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
              const next = tabs[(i + (e.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length];
              if (!next) return;
              setTab(next.id);
              document.getElementById(`tab-${next.id}`)?.focus();
            }}
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === "changeset" ? (
        <div role="tabpanel" id="panel-changeset" aria-labelledby="tab-changeset">
          <ChangeSetPanel taskId={taskId} />
        </div>
      ) : tab === "review" ? (
        <div role="tabpanel" id="panel-review" aria-labelledby="tab-review">
          {task.data ? <ReviewPanel taskId={taskId} repoIds={task.data.repoIds} /> : null}
        </div>
      ) : (
        <div role="tabpanel" id="panel-transcript" aria-labelledby="tab-transcript" className="space-y-4">
          {transcript.loaded ? (
            <Transcript events={transcript.events} capabilities={capabilities} />
          ) : (
            <p className="text-sm text-muted">Loading the transcript…</p>
          )}
          {task.data && idleStatuses.has(task.data.status) ? (
            <MessageComposer
              taskId={taskId}
              currentMode={task.data.mode as Mode}
              writeMode={task.data.mode === "ask" ? undefined : (task.data.mode as Mode)}
            />
          ) : null}
        </div>
      )}
    </section>
  );
}
