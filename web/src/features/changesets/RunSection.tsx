import * as Dialog from "@radix-ui/react-dialog";
import { useEffect, useRef, useState } from "react";
import { ApiError, describeError } from "../../api/client";
import type { ChangeSetDTO, ScopeDTO } from "../../api/gen/api.gen";
import { kindLabel } from "../proposals/core";
import { isNotReady } from "./api";
import { canSubmit, runItems, runSummary, stepRun, warningCodes, type RunCommand, type RunFlow, type RunInput } from "./core";
import { useChangeSetMutations } from "./hooks";

/** Confirm all / per-repo confirm with the hashes the person saw, warnings to acknowledge, live progress and Stop (RV-03, RV-04). */
export function RunSection({ cs, nameOf }: { cs: ChangeSetDTO; nameOf: (repoId: string) => string }) {
  const m = useChangeSetMutations(cs.id);
  const [flow, setFlow] = useState<RunFlow>({ tag: "idle" });
  const ref = useRef<RunFlow>(flow);
  const csRef = useRef(cs);
  csRef.current = cs;

  const exec = (c: RunCommand) => {
    if (c.tag === "preflight") {
      m.preflight.mutate(c.scope, {
        onSuccess: (r) => {
          dispatch({ tag: "preflighted", warnings: r.warnings });
        },
        onError: (e) => {
          dispatch({ tag: "preflightFailed", message: describeError(e) });
        },
      });
      return;
    }
    m.confirm.mutate(c.request, {
      onSuccess: () => {
        dispatch({ tag: "accepted" });
      },
      onError: (e) => {
        const text = isNotReady(e) ? "Confirming a change set isn't available yet." : describeError(e);
        dispatch({ tag: "rejected", conflict: e instanceof ApiError && e.status === 409 && e.code === "stale", message: text });
      },
    });
  };
  function dispatch(input: RunInput) {
    const [next, cmds] = stepRun(ref.current, input);
    ref.current = next;
    setFlow(next);
    cmds.forEach(exec);
  }

  // If the set changes while the dialog is open, nothing is sent until the person looks again.
  useEffect(() => {
    const cur = ref.current;
    if (cur.tag === "reviewing" || cur.tag === "loading") dispatch({ tag: "seen", items: runItems(cs, cur.scope) });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cs.members]);

  const open = (scope: ScopeDTO) => {
    dispatch({ tag: "open", scope, items: runItems(cs, scope) });
  };

  const running = cs.confirming !== undefined;
  const canRun = cs.state === "open" && !running;
  const allCount = runItems(cs, { kind: "all" }).length;
  const summary = runSummary(cs, (pid) => {
    const mem = cs.members.find((x) => x.proposalId === pid);
    return mem ? nameOf(mem.repoId) : undefined;
  });
  const orderedRepos = cs.order.filter((r) => runItems(cs, { kind: "repo", repoId: r }).length > 0);

  return (
    <section aria-labelledby="cs-run-title" className="space-y-2">
      <h3 id="cs-run-title" className="font-medium">
        Confirm
      </h3>
      <div className="flex flex-wrap gap-2">
        <button
          type="button"
          disabled={!canRun || allCount === 0}
          className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
          onClick={() => {
            open({ kind: "all" });
          }}
        >
          Confirm all{allCount > 0 ? ` (${String(allCount)})` : ""}
        </button>
        {orderedRepos.map((r) => (
          <button
            key={r}
            type="button"
            disabled={!canRun}
            className="rounded border border-line px-3 py-1 text-sm hover:bg-surface disabled:opacity-50"
            onClick={() => {
              open({ kind: "repo", repoId: r });
            }}
          >
            Confirm {nameOf(r)}
          </button>
        ))}
        {running ? (
          <button
            type="button"
            disabled={m.stop.isPending}
            className="rounded border border-danger px-3 py-1 text-sm text-danger"
            onClick={() => {
              m.stop.mutate();
            }}
          >
            Stop
          </button>
        ) : null}
      </div>
      {summary ? (
        <p role="status" className={`text-sm ${cs.run.state === "halted" && !running ? "text-danger" : "text-muted"}`}>
          {summary}
        </p>
      ) : null}
      {m.stop.error ? (
        <p role="alert" className="text-sm text-danger">
          {describeError(m.stop.error)}
        </p>
      ) : null}
      <RunDialog flow={flow} nameOf={nameOf} dispatch={dispatch} />
    </section>
  );
}

function RunDialog({ flow, nameOf, dispatch }: { flow: RunFlow; nameOf: (repoId: string) => string; dispatch: (i: RunInput) => void }) {
  const open = flow.tag !== "idle";
  const items = flow.tag === "idle" || flow.tag === "changed" ? [] : flow.items;
  const warnings = flow.tag === "reviewing" || flow.tag === "sending" ? flow.warnings : [];
  const acked = flow.tag === "reviewing" || flow.tag === "sending" ? flow.acked : [];
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(o) => {
        if (!o) dispatch({ tag: "cancel" });
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 bg-black/50" />
        <Dialog.Content
          role="alertdialog"
          className="fixed top-1/2 left-1/2 w-[36rem] max-w-[95vw] -translate-x-1/2 -translate-y-1/2 space-y-3 rounded-md border-2 border-line bg-canvas p-4 shadow-lg"
          onPointerDownOutside={(e) => {
            e.preventDefault();
          }}
          onOpenAutoFocus={(e) => {
            e.preventDefault();
            document.getElementById("cs-run-cancel")?.focus();
          }}
        >
          <Dialog.Title className="text-base font-semibold">
            {flow.tag !== "idle" && flow.scope.kind === "all" ? "Confirm all proposals" : "Confirm proposals of one repo"}
          </Dialog.Title>
          <Dialog.Description className="text-sm text-muted">
            These are confirmed in this order. It stops at the first one that does not complete. Nothing runs until you confirm.
          </Dialog.Description>
          {flow.tag === "loading" ? <p className="text-sm text-muted">Checking the order…</p> : null}
          {flow.tag === "changed" ? (
            <p role="alert" className="text-sm text-danger">
              This changed while you were looking. Close this and review it again before you confirm.
            </p>
          ) : (
            <ol aria-label="Proposals to confirm" className="list-decimal space-y-1 pl-5 text-sm">
              {items.map((it) => (
                <li key={it.proposalId}>
                  <span className="font-medium">{kindLabel(it.kind)}</span> in {nameOf(it.repoId)}{" "}
                  <span className="font-mono text-xs text-muted" title="The version you are confirming">
                    {it.hash.slice(0, 12)}
                  </span>
                </li>
              ))}
            </ol>
          )}
          {warningCodes(warnings).map((code) => (
            <div key={code} className="space-y-1 rounded border border-warn p-2 text-sm">
              <ul className="list-disc pl-5">
                {warnings
                  .filter((w) => w.code === code)
                  .map((w, i) => (
                    <li key={i}>{w.message}</li>
                  ))}
              </ul>
              <label className="flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={acked.includes(code)}
                  disabled={flow.tag !== "reviewing"}
                  onChange={(e) => {
                    dispatch({ tag: "ack", code, on: e.target.checked });
                  }}
                />
                I understand and want to go on
              </label>
            </div>
          ))}
          {flow.tag === "reviewing" && flow.error ? (
            <p role="alert" className="text-sm text-danger">
              {flow.error}
            </p>
          ) : null}
          <div className="flex justify-end gap-2">
            <button id="cs-run-cancel" type="button" className="rounded border border-line px-3 py-1 text-sm" onClick={() => { dispatch({ tag: "cancel" }); }}>
              {flow.tag === "changed" ? "Close" : "Cancel"}
            </button>
            {flow.tag !== "changed" ? (
              <button
                type="button"
                disabled={!canSubmit(flow)}
                className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
                onClick={() => {
                  dispatch({ tag: "submit" });
                }}
              >
                {flow.tag === "sending" ? "Confirming…" : "Confirm"}
              </button>
            ) : null}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
