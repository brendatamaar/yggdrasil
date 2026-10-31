import * as Dialog from "@radix-ui/react-dialog";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { describeError } from "../../api/client";
import * as api from "./api";
import { stepTrust, trustVisible, whyText, type TrustCommand, type TrustFlow, type TrustInput } from "./core";

const key = (repoId: string) => ["trust", repoId] as const;

/** Trust state of a repo's agent settings, with the what-would-run list (SF-09). Hidden when the repo ships none. */
export function TrustPanel({ repoId, repoName }: { repoId: string; repoName?: string }) {
  const qc = useQueryClient();
  const trust = useQuery({ queryKey: key(repoId), queryFn: () => api.fetchTrust(repoId) });
  const [flow, setFlow] = useState<TrustFlow>({ tag: "idle" });
  const flowRef = useRef<TrustFlow>(flow);
  const done = { onSettled: () => qc.invalidateQueries({ queryKey: key(repoId) }) };
  const set = useMutation({ mutationFn: (hash: string) => api.setTrust(repoId, hash), ...done });
  const revoke = useMutation({ mutationFn: () => api.revokeTrust(repoId), ...done });

  const run = (c: TrustCommand) => {
    set.mutate(c.hash, {
      onSuccess: () => {
        dispatch({ tag: "succeeded" });
      },
      onError: (err) => {
        dispatch({ tag: "failed", message: describeError(err) });
      },
    });
  };
  function dispatch(input: TrustInput) {
    const [next, cmds] = stepTrust(flowRef.current, input);
    flowRef.current = next;
    setFlow(next);
    cmds.forEach(run);
  }

  // The config may change under an open dialog: the flow notices the new hash.
  const liveHash = trust.data?.hash ?? "";
  useEffect(() => {
    if (flowRef.current.tag !== "reviewing") return;
    const [next] = stepTrust(flowRef.current, { tag: "seen", hash: liveHash });
    flowRef.current = next;
    setFlow(next);
  }, [liveHash]);

  const t = trust.data;
  if (trust.error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(trust.error)}
      </p>
    );
  }
  if (!t || !trustVisible(t)) return null;

  const name = repoName ?? "this repo";
  return (
    <section aria-label={`Agent settings of ${name}`} className="space-y-2 rounded border border-line p-3 text-sm">
      {t.state === "trusted" ? (
        <div className="flex items-center gap-3">
          <span>Agent settings in {name} are trusted.</span>
          <button
            type="button"
            className="rounded border border-line px-2 py-0.5"
            disabled={revoke.isPending}
            onClick={() => {
              revoke.mutate();
            }}
          >
            Revoke trust
          </button>
        </div>
      ) : (
        <div className="space-y-2">
          <p className="font-medium">{whyText(t.why)}</p>
          <p className="text-muted">Until you trust them, agents in {name} can only run in Ask mode.</p>
          <button
            type="button"
            className="rounded bg-accent px-3 py-1 font-medium text-canvas"
            onClick={() => {
              dispatch({ tag: "open", hash: t.hash ?? "" });
            }}
          >
            Review what would run
          </button>
        </div>
      )}
      {revoke.error ? (
        <p role="alert" className="text-danger">
          {describeError(revoke.error)}
        </p>
      ) : null}
      <Dialog.Root
        open={flow.tag !== "idle"}
        onOpenChange={(o) => {
          if (!o) dispatch({ tag: "cancel" });
        }}
      >
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 bg-black/50" />
          <Dialog.Content
            role="alertdialog"
            className="fixed top-1/2 left-1/2 max-h-[90vh] w-[36rem] max-w-[95vw] -translate-x-1/2 -translate-y-1/2 space-y-3 overflow-auto rounded-md border-2 border-danger bg-canvas p-4 shadow-lg"
            onPointerDownOutside={(e) => {
              e.preventDefault();
            }}
            onOpenAutoFocus={(e) => {
              e.preventDefault();
              document.getElementById("trust-cancel")?.focus();
            }}
          >
            <Dialog.Title className="text-base font-semibold">Trust the agent settings in {name}?</Dialog.Title>
            <Dialog.Description className="text-sm text-muted">
              These files start programs when an agent runs here. Only trust them if you know where they came from.
            </Dialog.Description>
            <ul className="space-y-3" aria-label="What would run">
              {t.files.map((f) => (
                <li key={f.path} className="space-y-1">
                  <p className="font-mono text-xs break-all">{f.path}</p>
                  {f.opaque ? <p className="text-danger">Could not be read as text. Yggdrasil cannot say what it runs.</p> : null}
                  <ul className="list-disc pl-5">
                    {f.wouldRun.map((w, i) => (
                      <li key={i} className="font-mono text-xs break-all">
                        {w}
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
            {flow.tag === "changed" ? (
              <p role="alert" className="text-danger">
                These files changed while you were looking. Review them again before you trust.
              </p>
            ) : null}
            {flow.tag === "failed" ? (
              <p role="alert" className="text-danger">
                {flow.message}
              </p>
            ) : null}
            <div className="flex justify-end gap-2">
              <button id="trust-cancel" type="button" className="rounded border border-line px-3 py-1" onClick={() => { dispatch({ tag: "cancel" }); }}>
                Cancel
              </button>
              {flow.tag === "changed" ? (
                <button type="button" className="rounded bg-accent px-3 py-1 font-medium text-canvas" onClick={() => { dispatch({ tag: "open", hash: liveHash }); }}>
                  Review again
                </button>
              ) : (
                <button
                  type="button"
                  disabled={flow.tag !== "reviewing" || t.why === "scan_incomplete"}
                  className="rounded bg-danger px-3 py-1 font-medium text-canvas disabled:opacity-50"
                  onClick={() => { dispatch({ tag: "confirm" }); }}
                >
                  {flow.tag === "sending" ? "Trusting…" : "Trust these settings"}
                </button>
              )}
            </div>
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </section>
  );
}
