import { describeError } from "../../api/client";
import { SafeLink } from "../../components/SafeLink";
import type { ChangeSetDTO } from "../../api/gen/api.gen";
import { describeProposal, kindLabel } from "../proposals/core";
import { isNotReady } from "./api";
import { moveRepo, sameOrder } from "./core";
import { useChangeSetMutations, useTaskChangeSet } from "./hooks";
import { LandingSection } from "./LandingDialogs";
import { RunSection } from "./RunSection";

/** The change set of a task (RV-03, RV-04): proposals by repo, merge order, confirm, linked MRs. */
export function ChangeSetPanel({ taskId }: { taskId: string }) {
  const q = useTaskChangeSet(taskId);
  let body;
  if (q.isPending) body = <p className="text-sm text-muted">Loading the change set…</p>;
  else if (q.error) {
    body = isNotReady(q.error) ? (
      <p role="status" className="text-sm text-muted">
        Change sets aren't available yet.
      </p>
    ) : (
      <p role="alert" className="text-sm text-danger">
        {describeError(q.error)}
      </p>
    );
  } else if (q.data) body = <Loaded cs={q.data} />;
  return (
    <div className="space-y-4">
      {body}
      <LandingSection taskId={taskId} />
    </div>
  );
}

const statusLabel = (status: string) => describeProposal({ status }).label;

function Loaded({ cs }: { cs: ChangeSetDTO }) {
  const m = useChangeSetMutations(cs.id);
  const names = new Map(cs.repos.map((r) => [r.repoId, r.name]));
  const nameOf = (id: string) => names.get(id) ?? id;
  const editable = cs.state === "open" && cs.confirming === undefined;
  const differs = !sameOrder(cs.order, cs.suggestedOrder) && cs.suggestedOrder.length === cs.order.length;
  const err = m.setOrder.error ?? m.close.error;

  return (
    <div className="space-y-4">
      <section aria-labelledby="cs-title" className="space-y-1">
        <div className="flex items-center gap-3">
          <h2 id="cs-title" className="font-medium">
            Change set
          </h2>
          <span className="rounded-full border border-line px-2 py-0.5 text-xs" data-testid="cs-state">
            {cs.state}
          </span>
          {cs.state === "open" ? (
            <button
              type="button"
              disabled={m.close.isPending || cs.confirming !== undefined}
              className="rounded border border-line px-2 py-0.5 text-sm hover:bg-surface disabled:opacity-50"
              onClick={() => {
                m.close.mutate();
              }}
            >
              Close change set
            </button>
          ) : null}
        </div>
        {cs.members.length === 0 ? <p className="text-sm text-muted">No proposals in this change set yet.</p> : null}
        {err ? (
          <p role="alert" className="text-sm text-danger">
            {describeError(err)}
          </p>
        ) : null}
      </section>

      <section aria-labelledby="cs-members-title" className="space-y-2">
        <h3 id="cs-members-title" className="font-medium">
          Proposals by repo
        </h3>
        {cs.order.map((repoId) => (
          <div key={repoId} role="group" aria-label={`Proposals in ${nameOf(repoId)}`} className="space-y-1">
            <p className="text-sm font-medium">{nameOf(repoId)}</p>
            <ul className="space-y-1">
              {cs.members
                .filter((x) => x.repoId === repoId)
                .map((x) => (
                  <li key={x.proposalId} className="flex flex-wrap items-center gap-2 rounded border border-line px-2 py-1 text-sm">
                    <span>{kindLabel(x.kind)}</span>
                    <span className="rounded-full border border-line px-2 text-xs text-muted">{statusLabel(x.status)}</span>
                  </li>
                ))}
            </ul>
          </div>
        ))}
      </section>

      <section aria-labelledby="cs-order-title" className="space-y-2">
        <h3 id="cs-order-title" className="font-medium">
          Merge order
        </h3>
        <ol aria-label="Merge order" className="space-y-1">
          {cs.order.map((repoId, i) => (
            <li key={repoId} className="flex items-center gap-2 text-sm">
              <span className="w-5 text-muted">{i + 1}.</span>
              <span className="flex-1">{nameOf(repoId)}</span>
              <button
                type="button"
                aria-label={`Move ${nameOf(repoId)} up`}
                disabled={!editable || i === 0 || m.setOrder.isPending}
                className="rounded border border-line px-2 disabled:opacity-40"
                onClick={() => {
                  m.setOrder.mutate(moveRepo(cs.order, i, -1));
                }}
              >
                Up
              </button>
              <button
                type="button"
                aria-label={`Move ${nameOf(repoId)} down`}
                disabled={!editable || i === cs.order.length - 1 || m.setOrder.isPending}
                className="rounded border border-line px-2 disabled:opacity-40"
                onClick={() => {
                  m.setOrder.mutate(moveRepo(cs.order, i, 1));
                }}
              >
                Down
              </button>
            </li>
          ))}
        </ol>
        {differs ? (
          <p className="text-sm text-muted">
            Suggested order: {cs.suggestedOrder.map(nameOf).join(", ")}.{" "}
            <button
              type="button"
              disabled={!editable || m.setOrder.isPending}
              className="underline disabled:opacity-50"
              onClick={() => {
                m.setOrder.mutate(cs.suggestedOrder);
              }}
            >
              Use suggested order
            </button>
          </p>
        ) : null}
        {cs.cycle && cs.cycle.length > 0 ? (
          <p className="text-sm text-warn">
            These repos depend on each other in a circle, so no order is suggested: {cs.cycle.map(nameOf).join(", ")}.
          </p>
        ) : null}
        {cs.warnings.length > 0 ? (
          <ul aria-label="Order warnings" className="list-disc space-y-1 pl-5 text-sm text-warn">
            {cs.warnings.map((w, i) => (
              <li key={i}>{w.message}</li>
            ))}
          </ul>
        ) : null}
      </section>

      <RunSection cs={cs} nameOf={nameOf} />

      <section aria-labelledby="cs-links-title" className="space-y-1">
        <h3 id="cs-links-title" className="font-medium">
          Merge requests
        </h3>
        {cs.links.length === 0 ? (
          <p className="text-sm text-muted">No merge requests created yet.</p>
        ) : (
          <>
            <ol aria-label="Linked merge requests" className="list-decimal space-y-1 pl-5 text-sm">
              {cs.links.map((l, i) => (
                <li key={`${l.repoId}-${String(i)}`}>
                  {nameOf(l.repoId)}:{" "}
                  {l.url ? (
                    <SafeLink href={l.url} className="underline">
                      {l.url}
                    </SafeLink>
                  ) : (
                    "created"
                  )}
                </li>
              ))}
            </ol>
            <p className="text-xs text-muted">Each merge request links back to the ones created before it, in this order.</p>
          </>
        )}
      </section>
    </div>
  );
}
