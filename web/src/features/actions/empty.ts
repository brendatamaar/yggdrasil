// Empty views are explicit success states (docs/design/phase-6-ui.md): distinct from
// pending, error and partial. An error never renders as an empty success.
export type EmptyKind =
  | "NoProjects"
  | "EmptyProject"
  | "NoRemote"
  | "NoCredentials"
  | "NoIssues"
  | "NoMRs"
  | "NoBuilds"
  | "NoTasks"
  | "NoStandup";

export const EMPTY_TEXT: Record<EmptyKind, string> = {
  NoProjects: "No projects yet. Add a project folder to get started.",
  EmptyProject: "This project has no repos yet.",
  NoRemote: "This repo has no remote, so there is nothing to read from a host.",
  NoCredentials: "Add a token for this host to read it. Local features still work.",
  NoIssues: "No open issues.",
  NoMRs: "No open merge requests.",
  NoBuilds: "No builds yet.",
  NoTasks: "No tasks yet.",
  NoStandup: "Nothing happened in this range.",
};

export type ViewKind = "pending" | "error" | "empty" | "ready";

/**
 * Which of the four views a list query is in. `count` is undefined until data arrived. An
 * error with no data is an error, never an empty list.
 */
export function viewKind(q: { isPending: boolean; error: unknown; count: number | undefined }): ViewKind {
  if (q.count === undefined) return q.error ? "error" : "pending";
  return q.count === 0 ? "empty" : "ready";
}
