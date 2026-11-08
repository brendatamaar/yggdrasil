import { describeError } from "../../api/client";
import { isNotAvailable } from "../../api/repoenv";
import { ComposeDown, ComposeFailed, ComposeNeedsConfirm, ComposeOff, ComposeStarting, ComposeStopping, ComposeUp } from "../../api/gen/api.gen";
import { portRange } from "./core";
import { useComposeAction, useCopyProfile, useWorktreeEnv } from "./hooks";

const btn = "rounded border border-line px-2 py-0.5 text-sm hover:bg-surface disabled:opacity-50";

const composeText: Record<string, string> = {
  [ComposeOff]: "Not used",
  [ComposeNeedsConfirm]: "Needs your confirmation",
  [ComposeDown]: "Down",
  [ComposeStarting]: "Starting",
  [ComposeUp]: "Up",
  [ComposeStopping]: "Stopping",
  [ComposeFailed]: "Failed",
};

const resultText: Record<string, string> = { copied: "Copied", skipped: "Skipped", failed: "Failed" };

/** One worktree's environment: its port block, the profile copy and its compose project (WE-01 to WE-03). */
export function WorktreeEnvPanel({ worktreeId, title }: { worktreeId: string; title: string }) {
  const q = useWorktreeEnv(worktreeId);
  const copy = useCopyProfile(worktreeId);
  const compose = useComposeAction(worktreeId);

  return (
    <section aria-label={`Environment of ${title}`} className="space-y-2 rounded border border-line p-3">
      <h3 className="font-medium">{title}</h3>
      {q.isPending ? <p className="text-sm text-muted">Loading…</p> : null}
      {q.isError ? (
        <p role="alert" className="text-sm text-danger">
          {describeError(q.error)}
        </p>
      ) : null}
      {q.data ? (
        <>
          <p className="text-sm">
            Port block:{" "}
            {portRange(q.data) ? <span className="font-mono">{portRange(q.data)}</span> : <span className="text-muted">none yet</span>}
          </p>

          <div className="space-y-1">
            <div className="flex items-center gap-3">
              <button type="button" className={btn} disabled={copy.isPending} onClick={() => { copy.mutate(); }}>
                {copy.isPending ? "Copying…" : "Copy profile files"}
              </button>
              {copy.error ? (
                <span role="alert" className="text-sm text-danger">
                  {describeError(copy.error)}
                </span>
              ) : null}
            </div>
            {q.data.profile.length > 0 ? (
              <ul aria-label="Last copy" className="text-sm">
                {q.data.profile.map((r) => (
                  <li key={r.path}>
                    <code>{r.path}</code>: {resultText[r.result] ?? r.result}
                    {r.reason ? <span className="text-muted">{` (${r.reason})`}</span> : null}
                  </li>
                ))}
              </ul>
            ) : null}
          </div>

          {q.data.compose !== ComposeOff ? (
            <div className="space-y-1">
              <p className="text-sm">
                Compose: <span className="font-medium">{composeText[q.data.compose] ?? q.data.compose}</span>
              </p>
              {q.data.composeNote ? <p className="text-sm text-warn">{q.data.composeNote}</p> : null}
              {q.data.compose === ComposeNeedsConfirm ? (
                <p className="text-sm text-warn">The compose file changed: review it and confirm.</p>
              ) : null}
              <div className="flex flex-wrap gap-2">
                {q.data.compose === ComposeNeedsConfirm ? (
                  <button type="button" className={btn} disabled={compose.isPending} onClick={() => { compose.mutate("confirm"); }}>
                    Confirm compose file
                  </button>
                ) : null}
                {q.data.compose === ComposeDown || q.data.compose === ComposeFailed ? (
                  <button type="button" className={btn} disabled={compose.isPending} onClick={() => { compose.mutate("up"); }}>
                    Start compose
                  </button>
                ) : null}
                {q.data.compose === ComposeUp ? (
                  <>
                    <button type="button" className={btn} disabled={compose.isPending} onClick={() => { compose.mutate("stop"); }}>
                      Stop compose
                    </button>
                    <button type="button" className={btn} disabled={compose.isPending} onClick={() => { compose.mutate("down"); }}>
                      Take compose down
                    </button>
                  </>
                ) : null}
              </div>
              {compose.error ? (
                <p role="alert" className="text-sm text-danger">
                  {isNotAvailable(compose.error)
                    ? "Docker Compose for worktrees isn't available yet."
                    : describeError(compose.error)}
                </p>
              ) : null}
            </div>
          ) : null}
        </>
      ) : null}
    </section>
  );
}
