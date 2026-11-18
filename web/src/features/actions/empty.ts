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
