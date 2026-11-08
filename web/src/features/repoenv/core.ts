import type { RepoEnvDTO } from "../../api/gen/api.gen";

// Client-side hints for the repo environment form and its save flow as a pure reducer.
// The server validates everything again; these hints only save a round trip.

/** A plain-language reason a profile path will be refused, or null. Mirrors the server's rules. */
export function pathHint(raw: string, existing: readonly string[] = []): string | null {
  const p = raw.trim();
  if (p === "") return "Enter a path, like .env";
  if (Array.from(p).some((c) => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127)) return "A path can't contain control characters.";
  if (/^([a-zA-Z]:|[\\/])/.test(p)) return "Use a path inside the repo, not an absolute one.";
  if (/[*?[\]]/.test(p)) return "Wildcards aren't supported. List each file.";
  const parts = p.split(/[\\/]+/);
  if (parts.includes("..")) return "A path can't leave the repo (no \"..\").";
  if (parts.some((s) => s.toLowerCase() === ".git")) return "Files inside .git can't be copied.";
  if (existing.includes(p)) return "That path is already in the list.";
  return null;
}

export interface Draft {
  profilePaths: string[];
  agentCopy: boolean;
  composeEnabled: boolean;
  composeFile: string;
}

export const toDraft = (env: RepoEnvDTO): Draft => ({
  profilePaths: [...env.profilePaths],
  agentCopy: env.agentCopy,
  composeEnabled: env.composeEnabled,
  composeFile: env.composeFile ?? "",
});

export const fromDraft = (repoId: string, d: Draft): RepoEnvDTO => ({
  repoId,
  profilePaths: d.profilePaths,
  agentCopy: d.agentCopy,
  composeEnabled: d.composeEnabled,
  ...(d.composeEnabled && d.composeFile.trim() !== "" ? { composeFile: d.composeFile.trim() } : {}),
});

/** Why the draft can't be saved yet, or null. */
export function draftProblem(d: Draft): string | null {
  if (d.composeEnabled) {
    if (d.composeFile.trim() === "") return "Enter the path of the compose file.";
    const hint = pathHint(d.composeFile);
    if (hint) return `Compose file: ${hint}`;
  }
  return null;
}

/** The ports of a worktree's block as "41000-41009", or null before one is allocated. */
export function portRange(env: { blockBase?: number; blockSize?: number }): string | null {
  if (env.blockBase === undefined || !env.blockSize) return null;
  return `${String(env.blockBase)}-${String(env.blockBase + env.blockSize - 1)}`;
}

export type FormState =
  | { tag: "editing"; draft: Draft; dirty: boolean }
  | { tag: "saving"; draft: Draft }
  | { tag: "failed"; draft: Draft; message: string };

export type FormInput =
  | { tag: "edit"; patch: Partial<Draft> }
  | { tag: "save" }
  | { tag: "saved"; env: RepoEnvDTO }
  | { tag: "saveFailed"; message: string }
  | { tag: "reload"; env: RepoEnvDTO };

export type FormCommand = { tag: "saveEnv"; draft: Draft };

export const formFrom = (env: RepoEnvDTO): FormState => ({ tag: "editing", draft: toDraft(env), dirty: false });

export function formStep(state: FormState, input: FormInput): { state: FormState; commands: FormCommand[] } {
  const stay = { state, commands: [] as FormCommand[] };
  switch (input.tag) {
    case "edit":
      if (state.tag === "saving") return stay;
      return { state: { tag: "editing", draft: { ...state.draft, ...input.patch }, dirty: true }, commands: [] };
    case "save":
      if (state.tag === "saving" || draftProblem(state.draft)) return stay;
      return { state: { tag: "saving", draft: state.draft }, commands: [{ tag: "saveEnv", draft: state.draft }] };
    case "saved":
      return state.tag === "saving" ? { state: formFrom(input.env), commands: [] } : stay;
    case "saveFailed":
      return state.tag === "saving"
        ? { state: { tag: "failed", draft: state.draft, message: input.message }, commands: [] }
        : stay;
    case "reload":
      // Fresh server data never overwrites unsaved edits or a save in flight.
      return state.tag === "editing" && !state.dirty ? { state: formFrom(input.env), commands: [] } : stay;
  }
}
