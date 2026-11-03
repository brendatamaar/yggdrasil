import * as Dialog from "@radix-ui/react-dialog";
import { useRef, useState } from "react";
import { describeError } from "../../api/client";
import type { WorktreeDTO } from "../../api/gen/api.gen";
import { isNotReady } from "./api";
import { stepLanding, type LandingCommand, type LandingFlow, type LandingInput, type LandingKind } from "./core";
import { useLandingMutations, useWorktrees } from "./hooks";

const statusText = (s: string) =>
  s === "active" ? "in use" : s === "merged" ? "merged" : s === "removing" ? "being removed" : s === "removed" ? "removed" : s;

/** Worktrees of the task with "Merge into main checkout" and "Remove worktree" (RV-05, RV-06). Both only create proposals. */
export function LandingSection({ taskId }: { taskId: string }) {
  const q = useWorktrees(taskId);
  const m = useLandingMutations();
  const [flow, setFlow] = useState<LandingFlow>({ tag: "closed" });
  const ref = useRef<LandingFlow>(flow);
  const worktrees = (q.data ?? []).filter((w) => w.status !== "removed");

  const exec = (c: LandingCommand) => {
    const done = {
      onSuccess: (p: { id: string }) => {
        dispatch({ tag: "created", proposalId: p.id });
      },
      onError: (e: unknown) => {
        dispatch({ tag: "failed", message: isNotReady(e) ? "This isn't available yet." : describeError(e) });
      },
    };
    if (c.kind === "merge_local") m.mergeLocal.mutate({ worktreeId: c.worktreeId, title: c.title }, done);
    else m.remove.mutate(c.worktreeId, done);
  };
  function dispatch(input: LandingInput) {
    const [next, cmds] = stepLanding(ref.current, input);
    ref.current = next;
    setFlow(next);
    cmds.forEach(exec);
  }

  if (q.error) {
    return isNotReady(q.error) ? null : (
      <p role="alert" className="text-sm text-danger">
        {describeError(q.error)}
      </p>
    );
  }
  if (worktrees.length === 0) return null;
  const current = flow.tag === "closed" ? undefined : worktrees.find((w) => w.id === flow.worktreeId);

  return (
    <section aria-labelledby="landing-title" className="space-y-2">
      <h3 id="landing-title" className="font-medium">
        Landing
      </h3>
      <ul className="space-y-1">
        {worktrees.map((w) => (
          <li key={w.id} className="flex flex-wrap items-center gap-2 text-sm">
            <span className="font-medium">{w.repoName}</span>
            <span className="font-mono text-xs text-muted">{w.branch}</span>
            <span className="rounded-full border border-line px-2 text-xs text-muted">{statusText(w.status)}</span>
            <button
              type="button"
              disabled={w.status !== "active"}
              className="rounded border border-line px-2 py-0.5 hover:bg-surface disabled:opacity-50"
              onClick={() => {
                dispatch({ tag: "open", kind: "merge_local", worktreeId: w.id, title: "" });
              }}
            >
              Merge {w.repoName} into main checkout
            </button>
            <button
              type="button"
              disabled={w.status === "removing"}
              className="rounded border border-line px-2 py-0.5 hover:bg-surface disabled:opacity-50"
              onClick={() => {
                dispatch({ tag: "open", kind: "remove_worktree", worktreeId: w.id, title: "" });
              }}
            >
              Remove {w.repoName} worktree
            </button>
          </li>
        ))}
      </ul>
      <LandingDialog flow={flow} worktree={current} dispatch={dispatch} />
    </section>
  );
}

const titles: Record<LandingKind, string> = { merge_local: "Merge into main checkout", remove_worktree: "Remove worktree" };

function LandingDialog({ flow, worktree, dispatch }: { flow: LandingFlow; worktree: WorktreeDTO | undefined; dispatch: (i: LandingInput) => void }) {
  const open = flow.tag !== "closed";
  const kind = flow.tag === "closed" ? undefined : flow.kind;
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(o) => {
        if (!o) dispatch({ tag: "close" });
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 bg-black/50" />
        <Dialog.Content className="fixed top-1/2 left-1/2 w-[32rem] max-w-[95vw] -translate-x-1/2 -translate-y-1/2 space-y-3 rounded-md border-2 border-line bg-canvas p-4 shadow-lg">
          <Dialog.Title className="text-base font-semibold">{kind ? titles[kind] : ""}</Dialog.Title>
          <Dialog.Description className="text-sm text-muted">
            {kind === "remove_worktree"
              ? "This creates a proposal to remove the worktree folder. Nothing is removed until you confirm it in the proposals list. The branch is kept."
              : "This creates a proposal to merge the branch into the main checkout. Nothing happens until you confirm it in the proposals list, and nothing is pushed."}
          </Dialog.Description>
          {worktree ? (
            <dl className="space-y-1 text-sm">
              <div>
                <dt className="inline text-muted">Repo: </dt>
                <dd className="inline">{worktree.repoName}</dd>
              </div>
              <div>
                <dt className="inline text-muted">Branch: </dt>
                <dd className="inline font-mono">{worktree.branch}</dd>
              </div>
              {kind === "merge_local" ? (
                <div>
                  <dt className="inline text-muted">Into: </dt>
                  <dd className="inline">{worktree.targetBranch ? (
                      <>
                        <span className="font-mono">{worktree.targetBranch}</span> (checked out in the main checkout of {worktree.repoName})
                      </>
                    ) : (
                      `the branch checked out in the main checkout of ${worktree.repoName}`
                    )}</dd>
                </div>
              ) : (
                <div>
                  <dt className="inline text-muted">Status: </dt>
                  <dd className="inline">{worktree.status === "merged" ? "merged" : "not merged yet"}</dd>
                  {worktree.clean === undefined ? null : (
                    <dd className="inline">{worktree.clean ? ", no uncommitted changes" : ", has uncommitted changes that will be lost"}</dd>
                  )}
                </div>
              )}
            </dl>
          ) : null}
          {flow.tag === "form" && flow.kind === "merge_local" ? (
            <label className="block text-sm">
              Merge commit title (optional)
              <input
                className="mt-1 w-full rounded border border-line bg-bg p-1"
                value={flow.title}
                maxLength={200}
                onChange={(e) => {
                  dispatch({ tag: "type", title: e.target.value });
                }}
              />
            </label>
          ) : null}
          {flow.tag === "form" && flow.error ? (
            <p role="alert" className="text-sm text-danger">
              {flow.error}
            </p>
          ) : null}
          {flow.tag === "proposed" ? (
            <p role="status" className="text-sm">
              Proposal created. Review and confirm it in the proposals list.
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <button
              type="button"
              className="rounded border border-line px-3 py-1 text-sm"
              onClick={() => {
                dispatch({ tag: "close" });
              }}
            >
              {flow.tag === "proposed" ? "Close" : "Cancel"}
            </button>
            {flow.tag !== "proposed" ? (
              <button
                type="button"
                disabled={flow.tag !== "form"}
                className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
                onClick={() => {
                  dispatch({ tag: "submit" });
                }}
              >
                {flow.tag === "sending" ? "Creating…" : "Create proposal"}
              </button>
            ) : null}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
