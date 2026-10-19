import { useState } from "react";
import { describeError } from "../../api/client";
import { relativeTime } from "../repos/ui";
import { DiffView } from "./DiffView";
import { useCommit, useLog } from "./hooks";
import { NavList, Notice } from "./parts";
import { itemBtn } from "./styles";

const PAGE = 50;

function CommitDetail({ repoId, oid }: { repoId: string; oid: string }) {
  const q = useCommit(repoId, oid);
  if (q.isPending) return <Notice>Loading commit…</Notice>;
  if (q.isError) return <Notice tone="danger">{describeError(q.error)}</Notice>;
  const { commit, body, files, diff } = q.data;
  return (
    <div className="min-w-0 space-y-2">
      <header>
        <h3 className="font-medium">{commit.subject}</h3>
        <p className="text-xs text-muted">
          <span className="font-mono">{commit.short}</span> · {commit.author} · {relativeTime(commit.at)}
        </p>
        {body.trim() && <pre className="mt-1 whitespace-pre-wrap text-sm">{body.trim()}</pre>}
      </header>
      <ul aria-label="Files in this commit" className="text-sm">
        {files.map((f) => (
          <li key={f.path} className="flex gap-2">
            <span className="w-4 font-mono text-xs text-warn">{f.status}</span>
            <span className="truncate" title={f.path}>{f.origPath ? `${f.origPath} → ${f.path}` : f.path}</span>
          </li>
        ))}
      </ul>
      <DiffView diff={diff} label={`Diff of ${commit.short}`} />
    </div>
  );
}

export function HistoryTab({ repoId }: { repoId: string }) {
  const [limit, setLimit] = useState(PAGE);
  const [oid, setOid] = useState<string | null>(null);
  const q = useLog(repoId, limit);
  if (q.isPending) return <Notice>Loading history…</Notice>;
  if (q.isError) return <Notice tone="danger">{describeError(q.error)}</Notice>;
  const commits = q.data.commits;
  if (commits.length === 0) return <Notice>No commits yet.</Notice>;
  return (
    <div className="grid gap-3 md:grid-cols-[minmax(14rem,24rem)_1fr]">
      <div className="space-y-2">
        <NavList label="Commits" className="divide-y divide-line rounded-md border border-line">
          {commits.map((c) => (
            <li key={c.oid}>
              <button type="button" data-nav aria-current={c.oid === oid} onClick={() => { setOid(c.oid); }} className={`${itemBtn(c.oid === oid)} flex-col items-start gap-0`}>
                <span className="w-full truncate">{c.subject}</span>
                <span className="text-xs text-muted">
                  <span className="font-mono">{c.short}</span> · {c.author} · {relativeTime(c.at)}
                </span>
              </button>
            </li>
          ))}
        </NavList>
        {commits.length >= limit && (
          <button type="button" className="rounded-md border border-line px-3 py-1 text-sm hover:bg-surface" onClick={() => { setLimit(limit + PAGE); }}>
            Show more
          </button>
        )}
      </div>
      {oid ? <CommitDetail repoId={repoId} oid={oid} /> : <Notice>Select a commit to see what changed.</Notice>}
    </div>
  );
}
