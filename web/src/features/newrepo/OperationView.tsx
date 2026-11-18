import * as Dialog from "@radix-ui/react-dialog";
import { useState } from "react";
import type { NewRepoOperationDTO } from "../../api/gen/api.gen";
import { SafeLink } from "../../components/SafeLink";
import { ProposalsByIds } from "../proposals";
import { describeOperation, STAGE_STATUS_LABEL, stageLabel, type Tone } from "./core";
import { describeNewRepoError, useOperation, useRetryMachine } from "./hooks";
import { PublishPanel } from "./PublishPanel";
import { useSpecMemory } from "./store";
import { btn, Footer, Hash, Lines, primary } from "./ui";

const toneClass: Record<Tone, string> = {
  waiting: "border-accent text-accent",
  running: "border-line text-muted",
  done: "border-line text-fg",
  // Unknown is its own look and its own words: it is not a failure and not a success.
  unknown: "border-warn text-warn",
  problem: "border-danger text-danger",
};

function Stages({ op }: { op: NewRepoOperationDTO }) {
  if (!op.stages || op.stages.length === 0) return null;
  return (
    <ol aria-label="Progress" className="space-y-1 text-sm">
      {op.stages.map((s) => (
        <li key={s.name} data-stage-status={s.status} className="flex justify-between gap-2">
          <span>{stageLabel(s.name)}</span>
          <span className={s.status === "failed" ? "text-danger" : s.status === "unknown" ? "text-warn" : "text-muted"}>
            {STAGE_STATUS_LABEL[s.status] ?? s.status}
          </span>
        </li>
      ))}
    </ol>
  );
}

function RetryDialog({ operationId, kind }: { operationId: string; kind: "init" | "publish" }) {
  const machine = useRetryMachine(operationId);
  const spec = useSpecMemory((s) => s.specs[operationId]?.[kind]);
  const { state } = machine;
  return (
    <>
      <button
        type="button"
        className={primary}
        onClick={() => {
          machine.open(spec ? { hash: spec.hash, lines: spec.lines } : undefined);
        }}
      >
        Retry…
      </button>
      <Dialog.Root
        open={state.tag !== "closed"}
        onOpenChange={(o) => {
          if (!o) machine.close();
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 bg-black/50" />
          <Dialog.Content
            role="alertdialog"
            className="fixed top-1/2 left-1/2 w-[34rem] max-w-[95vw] -translate-x-1/2 -translate-y-1/2 space-y-3 rounded-md border-2 border-line bg-canvas p-4 text-fg shadow-lg"
          >
            <Dialog.Title className="text-base font-semibold">Retry this step?</Dialog.Title>
            <Dialog.Description className="text-sm text-muted">
              Nothing is retried automatically. Confirming asks Yggdrasil to check again that the step is still absent, then run it once.
            </Dialog.Description>
            {state.tag === "missing" ? (
              <p role="alert" className="text-sm text-warn">
                This page no longer holds the preview you confirmed (it is kept only until you reload), so it cannot confirm it again. Nothing was changed.
              </p>
            ) : state.tag !== "closed" ? (
              <>
                <Lines label="What will run again" lines={state.lines} />
                <Hash hash={state.hash} />
              </>
            ) : null}
            {state.tag === "failed" ? (
              <p role="alert" className="text-sm text-danger">
                {state.message}
              </p>
            ) : null}
            <Footer>
              <button type="button" className={btn} onClick={machine.close} disabled={state.tag === "sending"}>
                {state.tag === "missing" ? "Close" : "Cancel"}
              </button>
              {state.tag !== "missing" ? (
                <button type="button" className={primary} disabled={state.tag !== "reviewing" && state.tag !== "failed"} onClick={machine.confirm}>
                  {state.tag === "sending" ? "Retrying…" : "Confirm retry"}
                </button>
              ) : null}
            </Footer>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </>
  );
}

/** One operation, rendered from the backend's lifecycle variant (NR-01 to NR-04). */
export function OperationView({ operationId, onBack }: { operationId: string; onBack?: () => void }) {
  const q = useOperation(operationId);
  const [publishing, setPublishing] = useState(false);

  if (q.isPending) {
    return (
      <p role="status" className="text-sm text-muted">
        Loading…
      </p>
    );
  }
  if (q.isError) {
    return (
      <div className="space-y-2">
        <p role="alert" className="text-sm text-danger">
          {describeNewRepoError(q.error)}
        </p>
        <Footer>
          {onBack ? (
            <button type="button" className={btn} onClick={onBack}>
              Back
            </button>
          ) : null}
          <button
            type="button"
            className={btn}
            onClick={() => {
              void q.refetch();
            }}
          >
            Try again
          </button>
        </Footer>
      </div>
    );
  }

  const op = q.data;
  const v = describeOperation(op);
  return (
    <div className="space-y-3" data-operation-state={v.variant}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{op.leaf}</span>
        <span role="status" className={`rounded-full border px-2 py-0.5 text-xs ${toneClass[v.tone]}`}>
          {v.headline}
        </span>
      </div>
      <p className="text-sm whitespace-pre-wrap">{v.detail}</p>
      <Stages op={op} />
      {v.remoteUrl ? (
        <p className="text-sm">
          Remote:{" "}
          <SafeLink href={v.remoteUrl} className="text-accent underline">
            {v.remoteUrl}
          </SafeLink>
        </p>
      ) : null}
      {v.next.tag === "confirm" ? <ProposalsByIds ids={[v.next.proposalId]} /> : null}
      {v.next.tag === "publish" ? (
        publishing ? (
          <PublishPanel
            operation={op}
            onClose={() => {
              setPublishing(false);
            }}
          />
        ) : (
          <Footer>
            <button
              type="button"
              className={primary}
              onClick={() => {
                setPublishing(true);
              }}
            >
              Publish…
            </button>
          </Footer>
        )
      ) : null}
      <Footer>
        {onBack ? (
          <button type="button" className={btn} onClick={onBack}>
            Back
          </button>
        ) : null}
        {v.next.tag === "retry" ? <RetryDialog operationId={op.operationId} kind={v.next.kind} /> : null}
      </Footer>
    </div>
  );
}
