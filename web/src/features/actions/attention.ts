import {
  ActionDiagnoseBuild,
  ActionDiagnosePipeline,
  ActionDiagnoseRepo,
  ActionProposeCommits,
  ActionPullAndCheck,
  ReasonBehind,
  ReasonConflicts,
  ReasonDirty,
  ReasonDiverged,
  ReasonFailedBuild,
  ReasonFailedCI,
  ReasonUnreadable,
  type ActionKind,
  type ActionOfferingDTO,
  type AttentionReason,
  type RepoStatusDTO,
} from "../../api/gen/api.gen";

// Pure mapping between a Needs attention reason and the actions that answer it
// (docs/design/phase-6-actions.md: every reason has a useful Ask fallback).

/** The safe default offering for each reason when the catalogue is missing or silent. The server still validates it. */
export const FALLBACK_KIND: Record<AttentionReason, ActionKind> = {
  [ReasonUnreadable]: ActionDiagnoseRepo,
  [ReasonConflicts]: ActionDiagnoseRepo,
  [ReasonDiverged]: ActionDiagnoseRepo,
  [ReasonFailedCI]: ActionDiagnosePipeline,
  [ReasonFailedBuild]: ActionDiagnoseBuild,
  [ReasonBehind]: ActionPullAndCheck,
  [ReasonDirty]: ActionProposeCommits,
};

export const REASON_LABEL: Record<AttentionReason, string> = {
  [ReasonUnreadable]: "Cannot read this repo",
  [ReasonConflicts]: "Has conflicts",
  [ReasonDiverged]: "Diverged from upstream",
  [ReasonFailedCI]: "Pipeline failed",
  [ReasonFailedBuild]: "Latest build failed",
  [ReasonBehind]: "Behind upstream",
  [ReasonDirty]: "Uncommitted changes",
};

/** Plain titles for the fallback offerings and the palette. */
export const KIND_TITLE: Record<ActionKind, string> = {
  diagnose_pipeline: "Diagnose with agent",
  fix_pipeline: "Fix in worktree",
  review_mr: "Review with agent",
  propose_commits: "Propose commits",
  pull_and_check: "Pull and check",
  import_issue: "Add draft task",
  diagnose_build: "Diagnose build",
  diagnose_repo: "Diagnose repo",
  propose_ignore: "Review ignore suggestions",
};

export const kindTitle = (kind: ActionKind): string => KIND_TITLE[kind] ?? "Run action";

/** Reason codes of a repo, most urgent first; the same order the widget shows reasons in. */
export function reasonCodes(r: RepoStatusDTO, failedBuild: boolean): AttentionReason[] {
  const out: AttentionReason[] = [];
  if (r.view === "problem") out.push(ReasonUnreadable);
  if (r.conflicted > 0 || r.flags.includes("conflicts")) out.push(ReasonConflicts);
  if (r.flags.includes("diverged")) out.push(ReasonDiverged);
  if (r.failedPipeline) out.push(ReasonFailedCI);
  if (failedBuild) out.push(ReasonFailedBuild);
  if (r.behind > 0) out.push(ReasonBehind);
  if (r.staged + r.unstaged + r.untracked > 0) out.push(ReasonDirty);
  return out;
}

export interface ReasonActions {
  reason: AttentionReason;
  offerings: ActionOfferingDTO[];
  /** True when the catalogue had nothing for the reason and the fallback stands in. */
  fallback: boolean;
}

/**
 * Offerings per reason: the catalogue's own, or one safe fallback so no reason is ever
 * action-less. `sourceId` pins a failed build when the widget knows it.
 */
export function actionsForReasons(
  reasons: readonly AttentionReason[],
  catalogue: readonly ActionOfferingDTO[] | undefined,
  pins: Partial<Record<AttentionReason, string>> = {},
): ReasonActions[] {
  return reasons.map((reason) => {
    const own = (catalogue ?? []).filter((o) => o.reason === reason);
    if (own.length > 0) return { reason, offerings: own, fallback: false };
    const kind = FALLBACK_KIND[reason] ?? ActionDiagnoseRepo;
    const sourceId = pins[reason];
    const fb: ActionOfferingDTO = {
      kind,
      reason,
      title: kindTitle(kind),
      context: "metadata_only",
      ...(sourceId ? { sourceId } : {}),
      ...(reason === ReasonUnreadable ? { waitingForFolder: true } : {}),
    };
    return { reason, offerings: [fb], fallback: true };
  });
}
