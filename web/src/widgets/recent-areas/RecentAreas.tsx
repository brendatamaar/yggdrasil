import { Link } from "@tanstack/react-router";
import { describeError } from "../../api/client";
import { useRepos } from "../../features/repos/hooks";
import { relativeTime } from "../../features/repos/ui";

const LIMIT = 6;

/** Repos worked on most recently (by last commit), with their project. */
export function RecentAreas() {
  const { data, error, isPending } = useRepos("all");

  if (isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(error)}
      </p>
    );
  }
  const rows = data.projects
    .flatMap((p) => p.repos.map((r) => ({ project: p.name, repo: r })))
    .filter((x) => x.repo.lastCommit)
    .sort((a, b) => (b.repo.lastCommit?.at ?? "").localeCompare(a.repo.lastCommit?.at ?? ""))
    .slice(0, LIMIT);
  if (rows.length === 0) return <p className="text-sm text-muted">No recent work yet. Track a project to see it here.</p>;
  return (
    <ul className="space-y-1 text-sm">
      {rows.map(({ project, repo }) => (
        <li key={repo.id} className="flex flex-wrap items-baseline gap-2">
          <span className="text-muted">{project}</span>
          <Link to="/repos/$repoId" params={{ repoId: repo.id }} className="text-accent hover:underline">
            {repo.name}
          </Link>
          {repo.lastCommit ? (
            <span className="ml-auto text-xs text-muted">{relativeTime(repo.lastCommit.at)}</span>
          ) : null}
        </li>
      ))}
    </ul>
  );
}
