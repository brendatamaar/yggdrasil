import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { fetchBuilds } from "../../api/builds";
import { describeError } from "../../api/client";
import { useRepos } from "../../features/repos/hooks";
import { rankRepos } from "./rank";

const LIMIT = 8;

/** Repos that need a look, most urgent first. */
export function NeedsAttention() {
  const repos = useRepos("all");
  const builds = useQuery({ queryKey: ["builds", "list", "", ""], queryFn: () => fetchBuilds({}) });

  if (repos.isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (repos.error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(repos.error)}
      </p>
    );
  }
  const items = rankRepos(
    repos.data.projects.flatMap((p) => p.repos),
    builds.data ?? [],
  );

  if (items.length === 0) return <p className="text-sm text-muted">Nothing needs you right now.</p>;
  return (
    <div className="space-y-1">
      {builds.error ? (
        <p role="alert" className="text-xs text-danger">
          Builds could not be read: {describeError(builds.error)}
        </p>
      ) : null}
      <ul className="space-y-1 text-sm">
        {items.slice(0, LIMIT).map((it) => (
          <li key={it.repo.id} className="flex flex-wrap items-baseline gap-2">
            <Link to="/repos/$repoId" params={{ repoId: it.repo.id }} className="text-accent hover:underline">
              {it.repo.name}
            </Link>
            <span className="text-muted">{it.reasons.join(", ")}</span>
            {it.failedBuildId ? (
              <Link to="/builds/$buildId" params={{ buildId: it.failedBuildId }} className="ml-auto text-xs underline">
                View build
              </Link>
            ) : null}
          </li>
        ))}
      </ul>
    </div>
  );
}
