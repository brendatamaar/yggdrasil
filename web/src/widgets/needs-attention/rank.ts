import type { BuildDTO, RepoStatusDTO } from "../../api/gen/api.gen";

export interface Item {
  repo: RepoStatusDTO;
  /** Lower is more urgent. */
  urgency: number;
  reasons: string[];
  failedBuildId?: string;
}

/** Why a repo needs a look, and how urgent the most urgent reason is (lower first). */
export function reasonsFor(r: RepoStatusDTO, failedBuild: boolean): { urgency: number; reasons: string[] } {
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

/** The newest build of each repo, when that build failed. Maps repo id to build id. */
export function failedBuilds(builds: BuildDTO[]): Map<string, string> {
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

/** Repos that need a look, most urgent first; ties by name. */
export function rankRepos(repos: RepoStatusDTO[], builds: BuildDTO[]): Item[] {
  const failed = failedBuilds(builds);
  const items: Item[] = [];
  for (const r of repos) {
    const failedBuildId = failed.get(r.id);
    const { urgency, reasons } = reasonsFor(r, failedBuildId !== undefined);
    if (reasons.length > 0) items.push({ repo: r, urgency, reasons, ...(failedBuildId ? { failedBuildId } : {}) });
  }
  items.sort((a, b) => a.urgency - b.urgency || a.repo.name.localeCompare(b.repo.name));
  return items;
}
