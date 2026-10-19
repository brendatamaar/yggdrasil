import type { RepoHostResponse } from "../../api/gen/api.gen";
import { describeError } from "../../api/client";
import { relativeTime } from "../repos/ui";
import { useRepoHost } from "./hooks";
import { Notice } from "./parts";

const ext = { target: "_blank", rel: "noreferrer noopener" } as const;
const link = "text-accent underline";

const blockText: Record<string, string> = {
  unauthorized: "Add a token for this host to see merge requests and pipelines.",
  not_found: "The host can't find this project. Check the remote URL.",
  unsupported: "This host isn't supported yet. The links below still work.",
};

function Links({ host }: { host: RepoHostResponse }) {
  const l = host.links;
  return (
    <nav aria-label="Host links" className="flex flex-wrap gap-3 text-sm">
      <a className={link} href={l.repo} {...ext}>Repo</a>
      {l.branch && <a className={link} href={l.branch} {...ext}>Branch</a>}
      <a className={link} href={l.mrList} {...ext}>Merge requests</a>
      <a className={link} href={l.pipelines} {...ext}>Pipelines</a>
    </nav>
  );
}

function PhaseNote({ host }: { host: RepoHostResponse }) {
  if (host.phase === "blocked")
    return <Notice tone="warn">{blockText[host.blockReason ?? ""] ?? "Host data is paused. Refresh to try again."}</Notice>;
  if (host.phase === "retrying")
    return (
      <Notice tone="warn">
        The host isn't answering right now. Showing the last known data
        {host.retryAt ? `; retrying at ${new Date(host.retryAt).toLocaleTimeString()}` : ""}.
      </Notice>
    );
  return null;
}

/** The current branch's MR and pipeline (GH-02). */
export function HostBlock({ repoId }: { repoId: string }) {
  const q = useRepoHost(repoId);
  if (q.isPending) return <Notice>Loading host info…</Notice>;
  if (q.isError) return <Notice tone="danger">{describeError(q.error)}</Notice>;
  const host = q.data;
  return (
    <section aria-label="Host" className="space-y-1 rounded-md border border-line p-3">
      <h2 className="text-sm font-medium">
        Host{host.project ? <span className="ml-2 font-normal text-muted">{host.project}</span> : null}
      </h2>
      {host.credentialsInRemote && (
        <Notice tone="warn">The remote URL contains credentials. Use a credential helper and remove them from the URL.</Notice>
      )}
      {!host.linked ? (
        <Notice>{host.notLinked ?? "This repo isn't linked to a host."}</Notice>
      ) : (
        <>
          <PhaseNote host={host} />
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
            <dt className="text-muted">Merge request</dt>
            <dd>
              {host.mr ? (
                <>
                  <a className={link} href={host.mr.url} {...ext}>
                    #{host.mr.number} {host.mr.title}
                  </a>{" "}
                  <span className="rounded-sm border border-line px-1 text-xs">{host.mr.state}</span>
                </>
              ) : (
                <span className="text-muted">None for this branch</span>
              )}
            </dd>
            <dt className="text-muted">Pipeline</dt>
            <dd>
              {host.pipeline ? (
                <>
                  <span className={host.pipeline.status === "failed" ? "text-danger" : host.pipeline.status === "success" ? "text-ok" : ""}>
                    {host.pipeline.status}
                  </span>{" "}
                  <a className={link} href={host.pipeline.url} {...ext}>
                    Open pipeline
                  </a>{" "}
                  <span className="text-muted">{relativeTime(host.pipeline.updatedAt)}</span>
                </>
              ) : (
                <span className="text-muted">None for this branch</span>
              )}
            </dd>
          </dl>
        </>
      )}
      <Links host={host} />
    </section>
  );
}
