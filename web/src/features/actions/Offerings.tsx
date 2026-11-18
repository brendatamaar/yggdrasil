import { useMemo } from "react";
import type { ActionOfferingDTO, AttentionReason } from "../../api/gen/api.gen";
import { describeError } from "../../api/client";
import { actionsForReasons, REASON_LABEL } from "./attention";
import { ActionDialog } from "./ActionDialog";
import type { ActionTarget } from "./core";
import { targetOf, useActionMachine, useActionPaletteCommands, useRepoActions } from "./hooks";

const small = "rounded border border-line px-2 py-0.5 text-xs hover:border-accent";

function OfferingButton({
  o,
  onOpen,
}: {
  o: ActionOfferingDTO;
  onOpen: () => void;
}) {
  return (
    <span className="inline-flex items-center gap-1">
      <button type="button" className={small} onClick={onOpen}>
        {o.title}
      </button>
      {o.waitingForFolder ? <span className="text-xs text-warn">waits for folder</span> : null}
      {o.context === "metadata_only" && !o.waitingForFolder ? <span className="text-xs text-muted">repo facts only</span> : null}
    </span>
  );
}

/**
 * The buttons of one Needs attention row: for each reason the catalogue's offerings, or one
 * safe fallback when the catalogue has none (or could not be read), so no reason is without
 * an action. Opening one goes through the shared action dialog of the caller.
 */
export function AttentionRowActions({
  repoId,
  repoName,
  reasons,
  failedBuildId,
  open,
}: {
  repoId: string;
  repoName: string;
  reasons: readonly AttentionReason[];
  /** The newest failed local build of the repo, when known; the build action is pinned to it. */
  failedBuildId?: string;
  open: (t: ActionTarget) => void;
}) {
  const cat = useRepoActions(repoId);
  const offerings = cat.data?.offerings;
  const reasonKey = reasons.join(",");
  const rows = useMemo(() => {
    const seen = new Set<string>();
    return actionsForReasons(reasonKey === "" ? [] : (reasonKey.split(",")), offerings, failedBuildId ? { failed_build: failedBuildId } : {}).flatMap((g) =>
      g.offerings
        .filter((o) => {
          const key = `${o.kind}:${o.sourceId ?? ""}`;
          if (seen.has(key)) return false;
          seen.add(key);
          return true;
        })
        .map((o) => ({ reason: g.reason, o })),
    );
  }, [reasonKey, offerings, failedBuildId]);
  const flat = useMemo(() => rows.map((r) => r.o), [rows]);
  useActionPaletteCommands(repoId, repoName, flat, open);
  return (
    <ul aria-label={`Actions for ${repoName}`} className="flex flex-wrap gap-x-3 gap-y-1">
      {rows.map(({ reason, o }) => (
        <li key={`${reason}:${o.kind}:${o.sourceId ?? ""}`} data-reason={reason} title={REASON_LABEL[reason]}>
          <OfferingButton
            o={o}
            onOpen={() => {
              open(targetOf(repoId, o, repoName));
            }}
          />
        </li>
      ))}
    </ul>
  );
}

/** Every action the server offers for a repo (repo detail): one button each, the palette gets the same, the dialog is shared. */
export function RepoActions({ repoId, repoName }: { repoId: string; repoName: string }) {
  const machine = useActionMachine();
  const cat = useRepoActions(repoId);
  const offerings = cat.data?.offerings ?? [];
  useActionPaletteCommands(repoId, repoName, offerings, machine.open);

  return (
    <section aria-label="Agent actions" className="space-y-1">
      {cat.isPending ? <p className="text-sm text-muted">Loading actions…</p> : null}
      {cat.isError ? (
        <p role="alert" className="text-sm text-danger">
          Actions could not be loaded. {describeError(cat.error)}
        </p>
      ) : null}
      {cat.data && offerings.length === 0 ? <p className="text-sm text-muted">Nothing to act on in this repo right now.</p> : null}
      {offerings.length > 0 ? (
        <ul className="flex flex-wrap gap-x-3 gap-y-1">
          {offerings.map((o) => (
            <li key={`${o.kind}:${o.sourceId ?? ""}`}>
              <OfferingButton o={o} onOpen={() => { machine.open(targetOf(repoId, o, repoName)); }} />
              {o.note ? <span className="ml-1 text-xs text-muted">{o.note}</span> : null}
            </li>
          ))}
        </ul>
      ) : null}
      <ActionDialog machine={machine} />
    </section>
  );
}
