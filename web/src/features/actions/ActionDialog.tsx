import * as Dialog from "@radix-ui/react-dialog";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { ProposalsByIds } from "../proposals";
import type { ActionState } from "./core";
import { describePreview, shortHead } from "./describe";
import type { ActionMachine } from "./hooks";

const btn = "rounded border border-line px-3 py-1 text-sm";
const primary = "rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50";

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[6rem_1fr] gap-2 text-sm">
      <dt className="text-muted">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}

function titleOf(s: ActionState): string {
  switch (s.tag) {
    case "closed":
      return "Action";
    case "failed":
      return s.retry.target.title;
    default:
      return s.target.title;
  }
}

/**
 * The action dialog (AC-01 to AC-06): preview first, then one explicit submission. A result
 * that created proposals ends in the same review and confirm UI as every proposal; this
 * dialog has no execute button for a main checkout or a remote.
 */
export function ActionDialog({ machine }: { machine: ActionMachine }) {
  const { state } = machine;
  const open = state.tag !== "closed";
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(o) => {
        if (!o) machine.close();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 bg-black/50" />
        <Dialog.Content
          className="fixed top-1/2 left-1/2 max-h-[90vh] w-[40rem] max-w-[95vw] -translate-x-1/2 -translate-y-1/2 space-y-3 overflow-auto rounded-md border-2 border-line bg-canvas p-4 text-fg shadow-lg"
          aria-describedby="action-dialog-desc"
        >
          <Dialog.Title className="text-base font-semibold">{titleOf(state)}</Dialog.Title>
          <Dialog.Description id="action-dialog-desc" className="text-sm text-muted">
            Preview first. Nothing starts until you press the button.
          </Dialog.Description>
          <Body machine={machine} />
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function Body({ machine }: { machine: ActionMachine }) {
  const { state } = machine;
  switch (state.tag) {
    case "closed":
      return null;
    case "loading":
      return (
        <>
          <p role="status" className="text-sm text-muted">
            Preparing the preview…
          </p>
          <Footer>
            <button type="button" className={btn} onClick={machine.close}>
              Cancel
            </button>
          </Footer>
        </>
      );
    case "preview": {
      const v = describePreview(state.preview);
      return (
        <>
          <dl className="space-y-1">
            <Row label="Scope">{state.target.repoName ?? state.target.repoId}</Row>
            <Row label="Mode">{v.mode}</Row>
            <Row label="Source">{state.preview.source}</Row>
            {state.preview.expectedHead ? (
              <Row label="Pinned to">
                <span className="font-mono">{shortHead(state.preview.expectedHead)}</span>
              </Row>
            ) : null}
            <Row label="Will happen">{v.willHappen}</Row>
          </dl>
          {v.notes.length > 0 ? (
            <ul aria-label="Notes" className="list-disc space-y-1 pl-5 text-sm text-warn">
              {v.notes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          ) : null}
          {state.preview.contextText ? (
            <div className="space-y-1">
              <p className="text-xs text-muted">Attached context (plain text)</p>
              <pre
                tabIndex={0}
                aria-label="Attached context"
                className="max-h-56 overflow-auto rounded border border-line bg-surface p-2 font-mono text-xs break-words whitespace-pre-wrap"
              >
                {state.preview.contextText}
              </pre>
            </div>
          ) : null}
          <Footer>
            <button type="button" className={btn} onClick={machine.close}>
              Cancel
            </button>
            <button type="button" className={primary} onClick={machine.submit}>
              {v.submitLabel}
            </button>
          </Footer>
        </>
      );
    }
    case "submitting":
      return (
        <>
          <p role="status" className="text-sm text-muted">
            Sending… You can close this; it keeps going and will not be sent twice.
          </p>
          <Footer>
            <button type="button" className={btn} onClick={machine.close}>
              Close
            </button>
          </Footer>
        </>
      );
    case "succeeded": {
      const r = state.result;
      return (
        <>
          <p role="status" className="text-sm">
            {r.message}
          </p>
          {r.taskId ? (
            <p className="text-sm">
              <Link to="/tasks/$taskId" params={{ taskId: r.taskId }} className="text-accent underline" onClick={machine.close}>
                Open the task
              </Link>
            </p>
          ) : null}
          {r.proposalIds && r.proposalIds.length > 0 ? <ProposalsByIds ids={r.proposalIds} /> : null}
          <Footer>
            <button type="button" className={btn} onClick={machine.close}>
              Close
            </button>
          </Footer>
        </>
      );
    }
    case "failed":
      return (
        <>
          <p role="alert" className="text-sm text-danger">
            {state.message}
          </p>
          <Footer>
            <button type="button" className={btn} onClick={machine.close}>
              Close
            </button>
            <button type="button" className={primary} onClick={machine.retry}>
              {state.retry.tag === "submit" ? "Check and retry" : "Try again"}
            </button>
          </Footer>
        </>
      );
  }
}

function Footer({ children }: { children: ReactNode }) {
  return <div className="flex justify-end gap-2">{children}</div>;
}
