import { Link } from "@tanstack/react-router";
import { describeError } from "../../api/client";
import { useRepos } from "../../features/repos/hooks";
import { relativeTime } from "../../features/repos/ui";

const LIMIT = 8;

/** The latest commit of each tracked repo, newest first. */
export function GitActivity() {
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
    .flatMap((p) => p.repos)
    .flatMap((r) => (r.lastCommit ? [{ repo: r, commit: r.lastCommit }] : []))
    .sort((a, b) => b.commit.at.localeCompare(a.commit.at))
    .slice(0, LIMIT);
  if (rows.length === 0) return <p className="text-sm text-muted">No commits to show yet.</p>;
  return (
    <ul className="space-y-1 text-sm">
      {rows.map(({ repo, commit }) => (
        <li key={repo.id} className="flex flex-wrap items-baseline gap-2">
          <Link to="/repos/$repoId" params={{ repoId: repo.id }} className="text-accent hover:underline">
            {repo.name}
          </Link>
          <span className="min-w-0 truncate">{commit.subject}</span>
          <span className="ml-auto text-xs text-muted">
            {commit.author}, {relativeTime(commit.at)}
          </span>
        </li>
      ))}
    </ul>
  );
}
