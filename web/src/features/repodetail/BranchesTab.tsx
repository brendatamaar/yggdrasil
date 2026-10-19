import { describeError } from "../../api/client";
import type { BranchDTO } from "../../api/gen/api.gen";
import { useBranches } from "./hooks";
import { Notice } from "./parts";

function Group({ title, branches }: { title: string; branches: BranchDTO[] }) {
  return (
    <section aria-label={title}>
      <h3 className="mb-1 text-sm font-medium">{title}</h3>
      {branches.length === 0 ? (
        <Notice>None.</Notice>
      ) : (
        <ul className="divide-y divide-line rounded-md border border-line">
          {branches.map((b) => (
            <li key={b.name} className="flex flex-wrap items-center gap-2 px-2 py-1 text-sm">
              <span className="font-mono">{b.name}</span>
              {b.current && <span className="rounded-sm border border-accent px-1 text-xs text-accent">current</span>}
              {b.upstream && <span className="text-xs text-muted">tracks {b.upstream}</span>}
              <span className="ml-auto font-mono text-xs text-muted">{b.oid.slice(0, 7)}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function BranchesTab({ repoId, ahead, behind, hasUpstream }: { repoId: string; ahead: number; behind: number; hasUpstream: boolean }) {
  const q = useBranches(repoId);
  if (q.isPending) return <Notice>Loading branches…</Notice>;
  if (q.isError) return <Notice tone="danger">{describeError(q.error)}</Notice>;
  const bs = q.data.branches;
  return (
    <div className="space-y-3">
      {hasUpstream && (
        <p className="text-sm text-muted">
          Current branch: {String(ahead)} ahead / {String(behind)} behind its upstream.
        </p>
      )}
      <Group title="Local branches" branches={bs.filter((b) => !b.remote)} />
      <Group title="Remote branches" branches={bs.filter((b) => b.remote)} />
    </div>
  );
}
