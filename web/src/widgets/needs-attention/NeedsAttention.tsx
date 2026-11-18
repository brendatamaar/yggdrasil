import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { fetchBuilds } from "../../api/builds";
import { describeError } from "../../api/client";
import { ActionDialog, AttentionRowActions, EmptyNote, useActionMachine } from "../../features/actions";
import { useRepos } from "../../features/repos/hooks";
import { rankRepos } from "./rank";

const LIMIT = 8;

/** Repos that need a look, most urgent first. Each reason has an action that opens the shared preview dialog. */
export function NeedsAttention() {
  const repos = useRepos("all");
  const builds = useQuery({ queryKey: ["builds", "list", "", ""], queryFn: () => fetchBuilds({}) });
  const machine = useActionMachine();

  if (repos.isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (repos.error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(repos.error)}
      </p>
    );
  }
  const projects = repos.data.projects;
  if (projects.length === 0) return <EmptyNote kind="NoProjects" />;
  const all = projects.flatMap((p) => p.repos);
  if (all.length === 0) return <EmptyNote kind="EmptyProject" />;
  const items = rankRepos(all, builds.data ?? []);

  if (items.length === 0) return <p className="text-sm text-muted">Nothing needs you right now.</p>;
  return (
    <div className="space-y-1">
      {builds.error ? (
        <p role="alert" className="text-xs text-danger">
          Builds could not be read: {describeError(builds.error)}
        </p>
      ) : null}
      <ul className="space-y-2 text-sm">
        {items.slice(0, LIMIT).map((it) => (
          <li key={it.repo.id} className="space-y-1">
            <div className="flex flex-wrap items-baseline gap-2">
              <Link to="/repos/$repoId" params={{ repoId: it.repo.id }} className="text-accent hover:underline">
                {it.repo.name}
              </Link>
              <span className="text-muted">{it.reasons.join(", ")}</span>
              {it.failedBuildId ? (
                <Link to="/builds/$buildId" params={{ buildId: it.failedBuildId }} className="ml-auto text-xs underline">
                  View build
                </Link>
              ) : null}
            </div>
            <AttentionRowActions
              repoId={it.repo.id}
              repoName={it.repo.name}
              reasons={it.codes}
              {...(it.failedBuildId ? { failedBuildId: it.failedBuildId } : {})}
              open={machine.open}
            />
          </li>
        ))}
      </ul>
      <ActionDialog machine={machine} />
    </div>
  );
}
