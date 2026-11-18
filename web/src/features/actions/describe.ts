import type { ActionKind, ActionPreviewDTO } from "../../api/gen/api.gen";

/** True for an action whose effect ends in a proposal the person confirms (main checkout or remote). */
export const MUTATING_KINDS: ReadonlySet<ActionKind> = new Set(["pull_and_check", "propose_ignore", "propose_commits", "review_mr"]);

export interface PreviewView {
  /** Mode line: what the agent may do, or that no agent turn starts. */
  mode: string;
  /** What happens when the person presses the button. */
  willHappen: string;
  /** Label of the submit button. */
  submitLabel: string;
  /** Notes for gaps: metadata only, waiting for folder, truncation, labelled unavailable parts. */
  notes: string[];
}

const modeText = (mode: string | undefined): string => {
  switch (mode) {
    case "ask":
      return "Ask: the agent reads and answers. It cannot change your files.";
    case "code":
      return "Code: the agent edits files in its own isolated worktree. Your main checkout is not touched.";
    case undefined:
    case "":
      return "No agent turn starts.";
    default:
      return `Mode: ${mode}`;
  }
};

const will: Record<string, { text: string; submit: string }> = {
  diagnose_pipeline: { text: "Starts one Ask task with the failed job logs shown below.", submit: "Start diagnosis" },
  fix_pipeline: { text: "Starts one Code task in a new worktree to fix the failure.", submit: "Start fix" },
  review_mr: {
    text: "Starts one Ask task that reviews the merge request. Any comment it drafts waits as a proposal for you to confirm.",
    submit: "Start review",
  },
  propose_commits: {
    text: "Starts one Ask task that groups the current changes. Each commit it drafts waits as a proposal for you to confirm.",
    submit: "Start grouping",
  },
  pull_and_check: {
    text: "Creates a pull proposal (fast-forward only, never a merge). Nothing is pulled until you confirm it.",
    submit: "Create pull proposal",
  },
  import_issue: { text: "Creates a draft task. It does not start until you start it.", submit: "Create draft" },
  diagnose_build: { text: "Starts one Ask task with the build log shown below.", submit: "Start diagnosis" },
  diagnose_repo: { text: "Starts one Ask task with the repo facts shown below.", submit: "Start diagnosis" },
  propose_ignore: {
    text: "Creates an ignore proposal. Nothing is written until you confirm it.",
    submit: "Create ignore proposal",
  },
};

/** What the preview says in plain language. Pure. */
export function describePreview(p: ActionPreviewDTO): PreviewView {
  const w = will[p.kind] ?? { text: "Starts the action shown below.", submit: "Start" };
  const notes: string[] = [];
  if (p.waitingForFolder) {
    notes.push(
      "This repo cannot be read right now. The task would be saved as a draft and wait for the folder to be restored. It will not run until then.",
    );
  }
  if (p.context === "metadata_only") notes.push("Only repo facts are attached. The host details could not be read.");
  if (p.context === "none") notes.push("No context could be attached.");
  if (p.truncated) notes.push("The context is cut to fit the size limit.");
  for (const u of p.unavailable ?? []) notes.push(`Not available: ${u}`);
  return {
    mode: modeText(p.mode),
    willHappen: p.waitingForFolder ? `${w.text} It stays waiting for the folder.` : w.text,
    submitLabel: p.waitingForFolder ? "Save as waiting draft" : w.submit,
    notes,
  };
}

/** Short, display-only form of a commit hash. */
export const shortHead = (h: string | undefined): string => (h ? h.slice(0, 8) : "");
