import * as Dialog from "@radix-ui/react-dialog";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { ApiError, describeError } from "../../api/client";
import type { ProposalDTO } from "../../api/gen/api.gen";
import {
  describeProposal,
  groupByChangeSet,
  kindLabel,
  stepConfirm,
  type ConfirmCommand,
  type ConfirmInput,
  type ConfirmState,
} from "./core";
import { proposalKeys, useProposalMutations, useTaskProposals } from "./hooks";

/** Proposals of a task (RV-02): what each would run, confirm or reject, retry when it failed. */
export function ProposalsPanel({ taskId }: { taskId: string }) {
  const list = useTaskProposals(taskId);
  const qc = useQueryClient();
  const { confirm, reject, retry } = useProposalMutations();
  const [state, setState] = useState<ConfirmState>({ tag: "idle" });
  const stateRef = useRef<ConfirmState>(state);
  const proposals = list.data ?? [];

  const run = (c: ConfirmCommand) => {
    if (c.tag === "reload") {
      void qc.invalidateQueries({ queryKey: proposalKeys.all });
      return;
    }
    confirm.mutate(
      { id: c.id, hash: c.hash },
      {
        onSuccess: () => {
          dispatch({ tag: "succeeded" });
        },
        onError: (err) => {
          dispatch(err instanceof ApiError && err.status === 409 ? { tag: "conflict" } : { tag: "failed", message: describeError(err) });
        },
      },
    );
  };
  function dispatch(input: ConfirmInput) {
    const [next, cmds] = stepConfirm(stateRef.current, input);
    stateRef.current = next;
    setState(next);
    cmds.forEach(run);
  }

  // Tell the flow what the server shows now, so a changed hash is noticed while the dialog is open.
  useEffect(() => {
    const cur = stateRef.current;
    if (cur.tag !== "reviewing") return;
    const live = list.data?.find((p) => p.id === cur.id);
    const [next] = stepConfirm(cur, { tag: "seen", id: cur.id, hash: live?.actionHash ?? "", pending: live?.status === "pending" });
    if (next !== cur) {
      stateRef.current = next;
      setState(next);
    }
  }, [list.data]);

  if (list.isPending || proposals.length === 0) {
    return list.error ? (
      <p role="alert" className="text-sm text-danger">
        {describeError(list.error)}
      </p>
    ) : null;
  }

  const current = state.tag === "idle" ? undefined : proposals.find((p) => p.id === state.id);
  const mutationError = reject.error ?? retry.error;

  return (
    <section aria-labelledby="proposals-title" className="space-y-3">
      <h2 id="proposals-title" className="text-base font-semibold">
        Review
      </h2>
      {mutationError ? (
        <p role="alert" className="text-sm text-danger">
          {describeError(mutationError)}
        </p>
      ) : null}
      {groupByChangeSet(proposals).map((g) => (
        <div key={g.key} className="space-y-2">
          {g.changeSetId ? <p className="text-xs text-muted">Change set: these belong together ({g.items.length})</p> : null}
          <ul className="space-y-2">
            {g.items.map((p) => (
              <ProposalRow
                key={p.id}
                p={p}
                busy={reject.isPending || retry.isPending}
                onReview={() => {
                  dispatch({ tag: "open", id: p.id, hash: p.actionHash });
                }}
                onReject={() => {
                  reject.mutate(p.id);
                }}
                onRetry={() => {
                  retry.mutate(p.id);
                }}
              />
            ))}
          </ul>
        </div>
      ))}
      <ConfirmDialog
        state={state}
        proposal={current}
        onConfirm={() => {
          dispatch({ tag: "confirm" });
        }}
        onCancel={() => {
          dispatch({ tag: "cancel" });
        }}
        onReviewAgain={() => {
          if (current?.status === "pending") dispatch({ tag: "open", id: current.id, hash: current.actionHash });
          else dispatch({ tag: "cancel" });
        }}
      />
    </section>
  );
}

const toneClass = {
  waiting: "border-accent text-accent",
  running: "border-line text-muted",
  done: "border-line text-muted",
  muted: "border-line text-muted",
  problem: "border-danger text-danger",
} as const;

function ProposalRow({
  p,
  busy,
  onReview,
  onReject,
  onRetry,
}: {
  p: ProposalDTO;
  busy: boolean;
  onReview: () => void;
  onReject: () => void;
  onRetry: () => void;
}) {
  const v = describeProposal(p);
  return (
    <li className="space-y-1 rounded border border-line p-2 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{kindLabel(p.kind)}</span>
        {p.repoName ? <span className="text-muted">{p.repoName}</span> : null}
        <span className={`rounded-full border px-2 py-0.5 text-xs ${toneClass[v.tone]}`}>{v.label}</span>
        {p.fromAgent ? <span className="text-xs text-muted">Proposed by an agent</span> : null}
      </div>
      {p.summary ? <p className="whitespace-pre-wrap">{p.summary}</p> : null}
      {v.note ? <p className="whitespace-pre-wrap text-muted">{v.note}</p> : null}
      {p.result?.note ? <p className="whitespace-pre-wrap text-muted">{p.result.note}</p> : null}
      {p.result?.mrUrl ? <p className="text-muted">Merge request created.</p> : null}
      {v.actions.length > 0 ? (
        <div className="flex gap-2">
          {v.actions.includes("confirm") ? (
            <button type="button" className="rounded bg-accent px-3 py-1 font-medium text-canvas" onClick={onReview}>
              Review and confirm
            </button>
          ) : null}
          {v.actions.includes("reject") ? (
            <button type="button" disabled={busy} className="rounded border border-line px-3 py-1" onClick={onReject}>
              Reject
            </button>
          ) : null}
          {v.actions.includes("retry") ? (
            <button type="button" disabled={busy} className="rounded border border-line px-3 py-1" onClick={onRetry}>
              Retry
            </button>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

function ConfirmDialog({
  state,
  proposal,
  onConfirm,
  onCancel,
  onReviewAgain,
}: {
  state: ConfirmState;
  proposal: ProposalDTO | undefined;
  onConfirm: () => void;
  onCancel: () => void;
  onReviewAgain: () => void;
}) {
  const open = state.tag !== "idle";
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(o) => {
        if (!o) onCancel();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 bg-black/50" />
        <Dialog.Content
          role="alertdialog"
          className="fixed top-1/2 left-1/2 w-[34rem] max-w-[95vw] -translate-x-1/2 -translate-y-1/2 space-y-3 rounded-md border-2 border-line bg-canvas p-4 shadow-lg"
          onPointerDownOutside={(e) => {
            e.preventDefault();
          }}
          onOpenAutoFocus={(e) => {
            e.preventDefault();
            document.getElementById("proposal-cancel")?.focus();
          }}
        >
          <Dialog.Title className="text-base font-semibold">{proposal ? kindLabel(proposal.kind) : "Proposal"}</Dialog.Title>
          <Dialog.Description className="text-sm text-muted">
            This is exactly what will run. Nothing happens until you confirm.
          </Dialog.Description>
          {proposal ? (
            <div className="space-y-2 text-sm">
              {proposal.repoName ? <p>Repo: {proposal.repoName}</p> : null}
              {proposal.remote ? <p>Goes to: {proposal.remote}</p> : null}
              <ul className="list-disc space-y-1 pl-5" aria-label="What will run">
                {proposal.lines.map((l, i) => (
                  <li key={i} className="font-mono text-xs break-all">
                    {l}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
          {state.tag === "changed" ? (
            <p role="alert" className="text-sm text-danger">
              This changed while you were looking. Review it again before you confirm.
            </p>
          ) : null}
          {state.tag === "failed" ? (
            <p role="alert" className="text-sm text-danger">
              {state.message}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <button id="proposal-cancel" type="button" className="rounded border border-line px-3 py-1 text-sm" onClick={onCancel}>
              Cancel
            </button>
            {state.tag === "changed" ? (
              <button type="button" className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas" onClick={onReviewAgain}>
                Review again
              </button>
            ) : (
              <button
                type="button"
                disabled={state.tag !== "reviewing"}
                className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
                onClick={onConfirm}
              >
                {state.tag === "sending" ? "Confirming…" : "Confirm"}
              </button>
            )}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
