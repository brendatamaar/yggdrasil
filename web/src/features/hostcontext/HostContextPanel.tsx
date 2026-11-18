import { useState } from "react";
import { ActionDialog } from "../actions/ActionDialog";
import { useActionMachine, useRepoActions } from "../actions/hooks";
import { FailedJobsView, IssuesView, MRContextView } from "./Views";

const toggle = "rounded border border-line px-2 py-0.5 text-xs hover:border-accent aria-expanded:border-accent";

/**
 * Host context of a repo (GH-05, GH-06), read on demand: failed jobs and MR context appear
 * where the action catalogue pins a run or MR; issues open from a button. Nothing is polled.
 */
export function HostContextPanel({ repoId, repoName }: { repoId: string; repoName: string }) {
  const cat = useRepoActions(repoId);
  const machine = useActionMachine();
  const [open, setOpen] = useState<string | null>(null);
  const offerings = cat.data?.offerings ?? [];
  const ci = offerings.find((o) => o.kind === "diagnose_pipeline" && o.sourceId);
  const mr = offerings.find((o) => o.kind === "review_mr" && o.sourceId && Number.isInteger(Number(o.sourceId)));
  const flip = (k: string) => {
    setOpen((cur) => (cur === k ? null : k));
  };
  return (
    <section aria-label="Host context" className="space-y-2">
      <div className="flex flex-wrap gap-2">
        {ci?.sourceId ? (
          <button type="button" className={toggle} aria-expanded={open === "ci"} onClick={() => { flip("ci"); }}>
            Failed jobs
          </button>
        ) : null}
        {mr?.sourceId ? (
          <button type="button" className={toggle} aria-expanded={open === "mr"} onClick={() => { flip("mr"); }}>
            Merge request context
          </button>
        ) : null}
        <button type="button" className={toggle} aria-expanded={open === "issues"} onClick={() => { flip("issues"); }}>
          Issues
        </button>
      </div>
      {open === "ci" && ci?.sourceId ? <FailedJobsView repoId={repoId} repoName={repoName} runId={ci.sourceId} /> : null}
      {open === "mr" && mr?.sourceId ? <MRContextView repoId={repoId} repoName={repoName} number={Number(mr.sourceId)} /> : null}
      {open === "issues" ? (
        <IssuesView
          repoId={repoId}
          repoName={repoName}
          onImport={(issue) => {
            machine.open({ repoId, repoName, kind: "import_issue", sourceId: String(issue.number), title: `Add draft task: #${String(issue.number)} ${issue.title}` });
          }}
        />
      ) : null}
      <ActionDialog machine={machine} />
    </section>
  );
}
