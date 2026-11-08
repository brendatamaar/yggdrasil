import { describeError } from "../../api/client";
import { isNotReady, loadWorktreeDiff, type DiffLoader } from "./api";
import { sendable } from "./core";
import { useNotes, useReviewMutations, useVerifications } from "./hooks";
import { RepoDiff } from "./RepoDiff";
import { isNeedsOptIn, useOptInGate } from "./optin";
import { VerifyBadges } from "./VerifyBadges";
import type { Mode } from "../composer";

/**
 * The Review tab of a task: verification badges per repo first, then the
 * worktree diffs with inline notes (VF-02, VF-03, RV-01, PL-03).
 */
export function ReviewPanel({
  taskId,
  repoIds,
  mode,
  loadDiff = loadWorktreeDiff,
}: {
  taskId: string;
  repoIds: string[];
  /** The task's mode: a mode that needs the opt-in asks for it at the send click. */
  mode?: Mode;
  loadDiff?: DiffLoader;
}) {
  const verifs = useVerifications(taskId);
  const notes = useNotes(taskId);
  const m = useReviewMutations(taskId);
  const gate = useOptInGate(mode);

  const items = verifs.data?.verifications ?? [];
  const names = new Map(items.map((v) => [v.repoId, v.repoName || v.repoId]));
  const all = notes.data?.notes ?? [];
  const toSend = sendable(all);
  const sendNotes = (optIn: boolean) => {
    m.sendNotes.mutate(
      { ids: toSend.map((n) => n.id), optIn },
      {
        onError: (e) => {
          if (isNeedsOptIn(e)) gate.retry(sendNotes);
        },
      },
    );
  };
  const running = items.some((v) => v.state === "bootstrapping" || v.state === "verifying");
  const actionError = m.retry.error ?? m.sendNotes.error;

  return (
    <div className="space-y-4">
      <section aria-labelledby="verify-title" className="space-y-2">
        <div className="flex items-center gap-3">
          <h2 id="verify-title" className="font-medium">
            Verification
          </h2>
          <button
            type="button"
            className="rounded border border-line px-2 py-0.5 text-sm hover:bg-surface"
            disabled={m.retry.isPending || running}
            onClick={() => {
              m.retry.mutate();
            }}
          >
            Verify again
          </button>
        </div>
        {verifs.isPending ? <p className="text-sm text-muted">Loading…</p> : null}
        {verifs.error ? (
          isNotReady(verifs.error) ? (
            <p role="status" className="text-sm text-muted">Verification results aren't available yet.</p>
          ) : (
            <p role="alert" className="text-sm text-danger">{describeError(verifs.error)}</p>
          )
        ) : null}
        {verifs.data && items.length === 0 ? <p className="text-sm text-muted">Nothing has been verified yet.</p> : null}
        <VerifyBadges taskId={taskId} items={items} mode={mode} />
      </section>

      <section aria-labelledby="changes-title" className="space-y-2">
        <div className="flex items-center gap-3">
          <h2 id="changes-title" className="font-medium">
            Changes
          </h2>
          <button
            type="button"
            className="rounded border border-line px-2 py-0.5 text-sm hover:bg-surface disabled:opacity-50"
            disabled={toSend.length === 0 || m.sendNotes.isPending}
            onClick={() => {
              gate.request(sendNotes);
            }}
          >
            Send notes to agent{toSend.length > 0 ? ` (${String(toSend.length)})` : ""}
          </button>
          {m.sendNotes.isSuccess ? <span role="status" className="text-sm text-muted">Sent to the agent.</span> : null}
        </div>
        {notes.error && isNotReady(notes.error) ? (
          <p role="status" className="text-sm text-muted">Notes aren't available yet.</p>
        ) : null}
        {actionError ? (
          <p role="alert" className="text-sm text-danger">
            {describeError(actionError)}
          </p>
        ) : null}
        {repoIds.map((id) => (
          <RepoDiff key={id} taskId={taskId} repoId={id} repoName={names.get(id) ?? id} notes={all.filter((n) => n.repoId === id)} load={loadDiff} />
        ))}
      </section>
      {gate.dialog}
    </div>
  );
}
