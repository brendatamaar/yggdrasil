import type { UseQueryResult } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import type { HostContextStatusDTO, IssueDTO } from "../../api/gen/api.gen";
import { SafeLink } from "../../components/SafeLink";
import { EmptyNote } from "../actions/EmptyNote";
import { useFailedJobs, useIssues, useMRContext } from "./hooks";
import { describeStatus, readErrorText } from "./status";
import { StartDraftButton } from "./StartDraftButton";

const btn = "rounded border border-line px-2 py-0.5 text-xs hover:border-accent";

/** Scope and status around a host context read: pending, error with retry, blocked, partial, then the body. */
function Frame<T extends { status: HostContextStatusDTO }>({
  title,
  scope,
  q,
  children,
}: {
  title: string;
  scope: string;
  q: UseQueryResult<T>;
  children: (data: T) => ReactNode;
}) {
  return (
    <section aria-label={title} className="space-y-2 rounded-md border border-line p-3">
      <h3 className="text-sm font-medium">
        {title} <span className="font-normal text-muted">{scope}</span>
      </h3>
      <FrameBody q={q}>{children}</FrameBody>
    </section>
  );
}

function FrameBody<T extends { status: HostContextStatusDTO }>({ q, children }: { q: UseQueryResult<T>; children: (data: T) => ReactNode }) {
  if (q.isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (q.isError) {
    const e = readErrorText(q.error);
    return (
      <div role="alert" className="space-y-1 text-sm text-danger">
        <p>{e.text}</p>
        <button
          type="button"
          className={btn}
          onClick={() => {
            void q.refetch();
          }}
        >
          Try again
        </button>
      </div>
    );
  }
  const view = describeStatus(q.data.status);
  switch (view.tag) {
    case "blocked":
      if (view.reason === "no_token") return <EmptyNote kind="NoCredentials" />;
      return (
        <div className="space-y-1">
          <p role="status" data-blocked={view.reason} className="text-sm text-warn">
            {view.text}
          </p>
          {view.retryable ? (
            <button
              type="button"
              className={btn}
              onClick={() => {
                void q.refetch();
              }}
            >
              Try again
            </button>
          ) : null}
        </div>
      );
    case "waiting":
      return (
        <p role="status" className="text-sm text-warn">
          {view.text}
        </p>
      );
    case "partial":
      return (
        <div className="space-y-2">
          <p data-partial className="text-sm text-warn">
            Partial: {view.text}
          </p>
          {view.gaps.length > 0 ? (
            <ul aria-label="Missing parts" className="list-disc pl-5 text-xs text-muted">
              {view.gaps.map((g) => (
                <li key={g}>{g}</li>
              ))}
            </ul>
          ) : null}
          {children(q.data)}
        </div>
      );
    case "ready":
      return <>{children(q.data)}</>;
  }
}

const Truncated = ({ what }: { what: string }) => (
  <p data-truncated className="text-xs text-warn">
    {what} was cut to fit the size limit.
  </p>
);

const Plain = ({ label, text }: { label: string; text: string }) => (
  <pre
    tabIndex={0}
    aria-label={label}
    className="max-h-56 overflow-auto rounded border border-line bg-surface p-2 font-mono text-xs break-words whitespace-pre-wrap"
  >
    {text}
  </pre>
);

/** The failed jobs of one CI run, with redacted plain-text logs. */
export function FailedJobsView({ repoId, repoName, runId }: { repoId: string; repoName: string; runId: string }) {
  const q = useFailedJobs(repoId, runId);
  return (
    <Frame title="Failed jobs" scope={`${repoName}, run ${runId}`} q={q}>
      {(d) => (
        <div className="space-y-2">
          {d.truncated ? <Truncated what="The job list" /> : null}
          {d.jobs.length === 0 ? <p className="text-sm text-muted">No failed jobs were reported for this run.</p> : null}
          <ul className="space-y-2">
            {d.jobs.map((j) => (
              <li key={j.jobId} className="space-y-1">
                <p className="text-sm">
                  {j.name}
                  {j.stage ? <span className="text-muted"> ({j.stage})</span> : null}
                </p>
                {j.log ? <Plain label={`Log of ${j.name}`} text={j.log} /> : null}
                {j.logTruncated ? <Truncated what="This log" /> : null}
                {j.logUnavailable ? <p className="text-xs text-warn">Log not available: {j.logUnavailable}</p> : null}
              </li>
            ))}
          </ul>
        </div>
      )}
    </Frame>
  );
}

/** The MR, its pinned head and its diff, as plain text. */
export function MRContextView({ repoId, repoName, number }: { repoId: string; repoName: string; number: number }) {
  const q = useMRContext(repoId, number);
  return (
    <Frame title="Merge request" scope={`${repoName}, !${String(number)}`} q={q}>
      {(d) => (
        <div className="space-y-2">
          {d.mr ? (
            <p className="text-sm">
              {d.mr.title} <span className="text-muted">by {d.mr.author} ({d.mr.state})</span>
            </p>
          ) : null}
          {d.head ? <p className="text-xs text-muted">Pinned to head {d.head.slice(0, 8)}</p> : null}
          {d.diff ? <Plain label="Diff" text={d.diff} /> : <p className="text-sm text-muted">No diff was returned.</p>}
          {d.diffTruncated ? <Truncated what="The diff" /> : null}
        </div>
      )}
    </Frame>
  );
}

function IssueRow({ issue, repoId, onImport }: { issue: IssueDTO; repoId: string; onImport: (issue: IssueDTO) => void }) {
  return (
    <li className="space-y-1 text-sm">
      <div className="flex flex-wrap items-baseline gap-2">
        <span className="font-mono text-xs text-muted">#{issue.number}</span>
        <SafeLink href={issue.url} className="text-accent underline">
          {issue.title}
        </SafeLink>
        {issue.labels?.map((l) => (
          <span key={l} className="rounded-sm border border-line px-1 text-xs text-muted">
            {l}
          </span>
        ))}
        <span className="ml-auto flex items-center gap-2">
          {issue.draftTaskId ? (
            <>
              <Link to="/tasks/$taskId" params={{ taskId: issue.draftTaskId }} className="text-xs underline">
                Draft task
              </Link>
              <StartDraftButton taskId={issue.draftTaskId} repoId={repoId} />
            </>
          ) : (
            <button
              type="button"
              className={btn}
              onClick={() => {
                onImport(issue);
              }}
            >
              Add draft task
            </button>
          )}
        </span>
      </div>
      {issue.body ? <p className="line-clamp-3 whitespace-pre-wrap text-muted">{issue.body}</p> : null}
      {issue.bodyTruncated ? <Truncated what="This issue text" /> : null}
    </li>
  );
}

/**
 * Open issues of a repo (GH-06). `onImport` opens the shared action dialog for the issue; a
 * draft that exists can be started explicitly, never automatically.
 */
export function IssuesView({
  repoId,
  repoName,
  onImport,
}: {
  repoId: string;
  repoName: string;
  onImport: (issue: IssueDTO) => void;
}) {
  const q = useIssues(repoId);
  return (
    <Frame title="Issues" scope={repoName} q={q}>
      {(d) => (
        <div className="space-y-2">
          {d.truncated ? <Truncated what="The issue list" /> : null}
          {d.issues.length === 0 && d.status.phase === "ready" ? <EmptyNote kind="NoIssues" /> : null}
          <ul className="space-y-2">
            {d.issues.map((i) => (
              <IssueRow key={i.number} issue={i} repoId={repoId} onImport={onImport} />
            ))}
          </ul>
        </div>
      )}
    </Frame>
  );
}
