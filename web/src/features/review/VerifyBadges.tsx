import type { VerificationDTO } from "../../api/gen/api.gen";
import { describeError } from "../../api/client";
import { VerifyStateBadge } from "../../components/VerifyStateBadge";
import { isRunning } from "./core";
import { useReviewMutations } from "./hooks";
import { isNeedsOptIn, useOptInGate } from "./optin";
import type { Mode } from "../composer";

/** One repo's verification state: a badge with its attempt number. */
export function VerifyBadge({ v }: { v: VerificationDTO }) {
  return <VerifyStateBadge state={v.state} suffix={v.attempt && v.attempt > 0 ? ` · attempt ${String(v.attempt)}` : ""} />;
}

function Row({ v, taskId, mode }: { v: VerificationDTO; taskId: string; mode?: Mode }) {
  const m = useReviewMutations(taskId);
  const gate = useOptInGate(mode);
  const send = (optIn: boolean) => {
    m.sendFailure.mutate(
      { id: v.id, optIn },
      {
        onError: (e) => {
          if (isNeedsOptIn(e)) gate.retry(send);
        },
      },
    );
  };
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
                gate.request(send);
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
      {gate.dialog}
    </li>
  );
}

/** The verification badges of every repo of the task. */
export function VerifyBadges({ taskId, items, mode }: { taskId: string; items: VerificationDTO[]; mode?: Mode }) {
  return (
    <ul className="space-y-2">
      {items.map((v) => (
        <Row key={v.id} v={v} taskId={taskId} mode={mode} />
      ))}
    </ul>
  );
}
