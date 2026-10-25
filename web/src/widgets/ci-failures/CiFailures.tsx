import { useQueries } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { describeError } from "../../api/client";
import { fetchRepoHost } from "../../api/hosts";
import { SafeLink } from "../../components/SafeLink";
import { useRepos } from "../../features/repos/hooks";
import { relativeTime } from "../../features/repos/ui";

/** Repos whose current branch has a failed pipeline, with a link to the run. */
export function CiFailures() {
  const repos = useRepos("failed_pipeline");
  const failing = (repos.data?.projects ?? []).flatMap((p) => p.repos.filter((r) => r.failedPipeline));
  // The repo list only says "failed"; the host endpoint has the run's URL.
  const hosts = useQueries({
    queries: failing.map((r) => ({ queryKey: ["repos", "host", r.id], queryFn: () => fetchRepoHost(r.id) })),
  });

  if (repos.isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (repos.error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(repos.error)}
      </p>
    );
  }
  if (failing.length === 0) return <p className="text-sm text-muted">No failing pipelines.</p>;
  return (
    <ul className="space-y-1 text-sm">
      {failing.map((r, i) => {
        const host = hosts[i]?.data;
        const url = host?.pipeline?.url ?? host?.links.pipelines;
        return (
          <li key={r.id} className="flex flex-wrap items-baseline gap-2">
            <Link to="/repos/$repoId" params={{ repoId: r.id }} className="text-accent hover:underline">
              {r.name}
            </Link>
            <span className="text-muted">{r.branch}</span>
            {url ? (
              <SafeLink href={url} className="underline">
                View pipeline
              </SafeLink>
            ) : null}
            {host?.pipeline ? (
              <span className="ml-auto text-xs text-muted">{relativeTime(host.pipeline.updatedAt)}</span>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}
