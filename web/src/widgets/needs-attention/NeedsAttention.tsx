import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { fetchBuilds } from "../../api/builds";
import { describeError } from "../../api/client";
import type { BuildDTO, RepoStatusDTO } from "../../api/gen/api.gen";
import { useRepos } from "../../features/repos/hooks";

interface Item {
  repo: RepoStatusDTO;
  /** Lower is more urgent. */
  urgency: number;
  reasons: string[];
  failedBuildId?: string;
}

const LIMIT = 8;

function reasonsFor(r: RepoStatusDTO, failedBuild: boolean): { urgency: number; reasons: string[] } {
  const reasons: string[] = [];
  let urgency = 99;
  const add = (u: number, text: string) => {
    reasons.push(text);
    urgency = Math.min(urgency, u);
  };
  if (r.view === "problem") add(0, r.problemMessage ?? "Cannot read this repo");
  if (r.conflicted > 0 || r.flags.includes("conflicts")) add(0, "Has conflicts");
  if (r.flags.includes("diverged")) add(1, "Diverged from upstream");
  if (r.failedPipeline) add(2, "Pipeline failed");
  if (failedBuild) add(3, "Latest build failed");
  if (r.behind > 0) add(4, `${String(r.behind)} behind`);
  if (r.staged + r.unstaged + r.untracked > 0) add(5, "Uncommitted changes");
  return { urgency, reasons };
}

/** The newest build of each repo, when that build failed. */
function failedBuilds(builds: BuildDTO[]): Map<string, string> {
  const seen = new Set<string>();
  const failed = new Map<string, string>();
  for (const b of builds) {
    if (b.status === "queued" || b.status === "running") continue;
    for (const repoId of new Set(b.steps.map((s) => s.repoId))) {
      if (seen.has(repoId)) continue;
      seen.add(repoId);
      if (b.status === "failed") failed.set(repoId, b.id);
    }
  }
  return failed;
}

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
  const failed = failedBuilds(builds.data ?? []);
  const items: Item[] = [];
  for (const r of repos.data.projects.flatMap((p) => p.repos)) {
    const failedBuildId = failed.get(r.id);
    const { urgency, reasons } = reasonsFor(r, failedBuildId !== undefined);
    if (reasons.length > 0) items.push({ repo: r, urgency, reasons, ...(failedBuildId ? { failedBuildId } : {}) });
  }
  items.sort((a, b) => a.urgency - b.urgency || a.repo.name.localeCompare(b.repo.name));

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
