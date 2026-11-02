import type { VerificationDTO } from "../../api/gen/api.gen";
import { describeError } from "../../api/client";
import { VerifyStateBadge } from "../../components/VerifyStateBadge";
import { isRunning } from "./core";
import { useReviewMutations } from "./hooks";

/** One repo's verification state: a badge with its attempt number. */
export function VerifyBadge({ v }: { v: VerificationDTO }) {
  return <VerifyStateBadge state={v.state} suffix={v.attempt && v.attempt > 0 ? ` · attempt ${String(v.attempt)}` : ""} />;
}

function Row({ v, taskId }: { v: VerificationDTO; taskId: string }) {
  const m = useReviewMutations(taskId);
  const name = v.repoName || v.repoId;
  const err = m.sendFailure.error ?? m.cancel.error;
  return (
    <li className="space-y-1 rounded border border-line p-2" aria-label={`Verification of ${name}`}>
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-medium">{name}</span>
        <VerifyBadge v={v} />
        {v.state === "cancelled" && v.cancelCause === "not_trusted" ? (
          <span className="text-warn">Stopped: this repo is no longer trusted</span>
        ) : v.reason ? (
          <span className="text-muted">{v.reason}</span>
        ) : null}
        {isRunning(v) ? (
          <button
            type="button"
            className="rounded border border-line px-2 py-0.5 text-xs hover:bg-surface"
            disabled={m.cancel.isPending}
            onClick={() => {
              m.cancel.mutate(v.id);
            }}
          >
            Cancel verification
          </button>
        ) : null}
      </div>
      {v.state === "failed" || v.state === "interrupted" ? (
        <div className="space-y-1 text-sm">
          {v.failedStep ? (
            <p>
              Step: <span className="font-mono">{v.failedStep}</span>
              {v.exitCode !== undefined ? ` (exit code ${String(v.exitCode)})` : ""}
            </p>
          ) : null}
          {v.message ? (
            <pre tabIndex={0} aria-label={`Log tail of ${name}`} className="max-h-48 overflow-auto rounded border border-line bg-surface p-2 font-mono text-xs">
              {v.message}
            </pre>
          ) : null}
          {v.canSendFailure ? (
            <button
              type="button"
              className="rounded border border-line px-2 py-0.5 hover:bg-surface"
              disabled={m.sendFailure.isPending}
              onClick={() => {
                m.sendFailure.mutate(v.id);
              }}
            >
              Send failure to agent
            </button>
          ) : null}
          {m.sendFailure.isSuccess ? <p role="status" className="text-muted">Sent to the agent.</p> : null}
        </div>
      ) : null}
      {err ? (
        <p role="alert" className="text-sm text-danger">
          {describeError(err)}
        </p>
      ) : null}
    </li>
  );
}

/** The verification badges of every repo of the task. */
export function VerifyBadges({ taskId, items }: { taskId: string; items: VerificationDTO[] }) {
  return (
    <ul className="space-y-2">
      {items.map((v) => (
        <Row key={v.id} v={v} taskId={taskId} />
      ))}
    </ul>
  );
}
