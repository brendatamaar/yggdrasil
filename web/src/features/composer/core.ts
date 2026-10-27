// Pure decisions of the composer (docs/AGENTS.md 5.11): no IO, no clock.
// Components dispatch inputs and run the commands.

export type Mode = "ask" | "code" | "current_branch" | "project";
export type Scope = "repo" | "project";

export const MODE_LABEL: Record<Mode, string> = {
  ask: "Ask",
  code: "Code",
  current_branch: "Current branch",
  project: "Whole project",
};

export const MODE_HINT: Record<Mode, string> = {
  ask: "The agent can read, not change.",
  code: "The agent edits a separate worktree, not your checkout.",
  current_branch: "The agent edits your main checkout directly.",
  project: "The agent edits every tracked repo of the project, each in its own worktree.",
};

export const WRITE_MODES: readonly Mode[] = ["code", "current_branch", "project"];

/** Modes that need the person's explicit consent before the first use. */
export function needsOptIn(mode: Mode): boolean {
  return mode === "current_branch" || mode === "project";
}

export const OPT_IN_TEXT = {
  current_branch: {
    title: "Edit your main checkout?",
    body: "Current branch mode edits your main checkout directly. Only one editor per checkout is allowed, and builds and pulls on it wait until the task ends.",
  },
  project: {
    title: "Edit every repo of the project?",
    body: "Whole project mode edits every tracked repo of the project, each in a separate worktree.",
  },
} as const;

/** A project-wide mode always runs at project scope. */
export function effectiveScope(mode: Mode, scope: Scope): Scope {
  return mode === "project" ? "project" : scope;
}

export function visibleModes(writeModesOn: boolean): Mode[] {
  return writeModesOn ? ["ask", ...WRITE_MODES] : ["ask"];
}

/** Mode switch per message: Ask <-> the task's own write mode, nothing else. */
export function switchTargets(current: Mode, writeMode: Mode | undefined): Mode[] {
  if (current === "ask") return writeMode && writeMode !== "ask" ? [writeMode] : [];
  return ["ask"];
}

const MODEL_RE = /^[A-Za-z0-9.:-]{1,64}$/;

/** The model-name rule: letters, digits, dots, dashes, colons, up to 64. */
export function isValidModelName(name: string): boolean {
  return MODEL_RE.test(name);
}

export interface FormValues {
  prompt: string;
  mode: Mode;
  scope: Scope;
  projectId: string;
  repoId: string;
  agent: string;
  agentAvailable: boolean;
  /** "" means the agent's default. */
  model: string;
}

/** Why Start is disabled, or null when the form is valid. */
export function invalidReason(v: FormValues): string | null {
  if (v.prompt.trim() === "") return "Write what the agent should do.";
  if (v.projectId === "") return "Pick a project.";
  if (effectiveScope(v.mode, v.scope) === "repo" && v.repoId === "") return "Pick a repo.";
  if (v.agent === "" || !v.agentAvailable) return "Pick an available agent.";
  if (v.model !== "" && !isValidModelName(v.model)) {
    return "Model names use letters, digits, dots, dashes and colons, up to 64 characters.";
  }
  return null;
}

// --- Submit flow -----------------------------------------------------------

export type SubmitState =
  | { tag: "editing" }
  | { tag: "confirming"; mode: Mode }
  | { tag: "submitting" }
  | { tag: "failed"; message: string };

export type SubmitInput =
  | { tag: "submit"; mode: Mode; acknowledged: boolean }
  | { tag: "confirm" }
  | { tag: "cancel" }
  | { tag: "succeeded" }
  | { tag: "failed"; message: string; writeModesOff: boolean };

export type SubmitCommand =
  | { tag: "create"; optIn: boolean }
  | { tag: "acknowledge"; mode: Mode }
  | { tag: "hideWriteModes" };

export function stepSubmit(state: SubmitState, input: SubmitInput): [SubmitState, SubmitCommand[]] {
  switch (input.tag) {
    case "submit":
      if (state.tag === "submitting" || state.tag === "confirming") return [state, []];
      if (needsOptIn(input.mode) && !input.acknowledged) return [{ tag: "confirming", mode: input.mode }, []];
      return [{ tag: "submitting" }, [{ tag: "create", optIn: needsOptIn(input.mode) }]];
    case "confirm":
      if (state.tag !== "confirming") return [state, []];
      return [{ tag: "submitting" }, [{ tag: "acknowledge", mode: state.mode }, { tag: "create", optIn: true }]];
    case "cancel":
      return state.tag === "confirming" ? [{ tag: "editing" }, []] : [state, []];
    case "succeeded":
      return state.tag === "submitting" ? [{ tag: "editing" }, []] : [state, []];
    case "failed":
      if (state.tag !== "submitting") return [state, []];
      return [{ tag: "failed", message: input.message }, input.writeModesOff ? [{ tag: "hideWriteModes" }] : []];
  }
}

/** The server's answer when write modes are off (internal/api errWriteModes). */
export function isWriteModesRefusal(status: number, message: string): boolean {
  return status === 400 && message.includes("is not enabled in this version");
}

// --- Draft button ----------------------------------------------------------

export type DraftState =
  | { tag: "idle" }
  | { tag: "loading"; requestId: number }
  | { tag: "failed"; message: string };

export type DraftInput =
  | { tag: "start"; requestId: number }
  | { tag: "cancel" }
  | { tag: "done"; requestId: number }
  | { tag: "failed"; requestId: number; message: string };

export type DraftCommand = { tag: "request"; requestId: number } | { tag: "abort" } | { tag: "fill" };

export function stepDraft(state: DraftState, input: DraftInput): [DraftState, DraftCommand[]] {
  switch (input.tag) {
    case "start":
      if (state.tag === "loading") return [state, []];
      return [{ tag: "loading", requestId: input.requestId }, [{ tag: "request", requestId: input.requestId }]];
    case "cancel":
      return state.tag === "loading" ? [{ tag: "idle" }, [{ tag: "abort" }]] : [state, []];
    case "done":
      // A late answer for a cancelled request is dropped.
      return state.tag === "loading" && state.requestId === input.requestId
        ? [{ tag: "idle" }, [{ tag: "fill" }]]
        : [state, []];
    case "failed":
      return state.tag === "loading" && state.requestId === input.requestId
        ? [{ tag: "failed", message: input.message }, []]
        : [state, []];
  }
}

// --- @ mentions ------------------------------------------------------------

export interface MentionTrigger {
  /** Index of the "@" in the text. */
  start: number;
  query: string;
}

/** The @ token ending at `caret`, if the caret is inside one. */
export function findMention(text: string, caret: number): MentionTrigger | null {
  const before = text.slice(0, caret);
  const at = before.lastIndexOf("@");
  if (at < 0) return null;
  if (at > 0 && !/\s/.test(before.charAt(at - 1))) return null;
  const query = before.slice(at + 1);
  if (/\s/.test(query)) return null;
  return { start: at, query };
}

/** Replaces the token with `@repo/path` and a trailing space; returns text and caret. */
export function insertMention(text: string, caret: number, trigger: MentionTrigger, repoName: string, path: string) {
  const insert = `@${repoName}/${path} `;
  return { text: text.slice(0, trigger.start) + insert + text.slice(caret), caret: trigger.start + insert.length };
}

export function moveActive(active: number, count: number, nav: "down" | "up"): number {
  if (count === 0) return 0;
  return nav === "down" ? (active + 1) % count : (active - 1 + count) % count;
}
