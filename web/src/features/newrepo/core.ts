// Pure decisions of the new-repo and publish UI (docs/design/phase-6-ui.md and
// docs/design/phase-6-newrepos.md, docs/AGENTS.md 5.11): (state, input) -> (state, commands).
// No IO, no clock, no random values: request IDs arrive inside inputs. The operation's own
// lifecycle is the server's variant (NewRepoOperationDTO.state); this file only maps it to words.
import type { NewRepoOperationDTO } from "../../api/gen/api.gen";

// --- Prepare flow: preview, then one explicit submission ------------------------------------
// Used twice: init (form -> NewRepoPreviewResponse -> reserve) and publish
// (form -> PublishPreviewResponse -> pending publish proposal). The hash sent on submit is the
// one held with the preview the person was shown, never recomputed or re-read.

export type PrepareState<F, P extends { hash: string }, R> =
  | { tag: "editing" }
  | { tag: "previewing"; requestId: string; form: F }
  | { tag: "previewed"; requestId: string; form: F; preview: P }
  | { tag: "submitting"; requestId: string; form: F; preview: P }
  | { tag: "created"; result: R; preview: P }
  | { tag: "failed"; message: string; retry: PrepareRetry<F, P> };

/** What Retry does: preview again (new request), or send the same submission again (same request ID, so it cannot start twice). */
export type PrepareRetry<F, P> = { tag: "preview"; form: F } | { tag: "submit"; requestId: string; form: F; preview: P };

export type PrepareInput<F, P, R> =
  | { tag: "preview"; form: F; requestId: string }
  | { tag: "previewLoaded"; requestId: string; preview: P }
  | { tag: "previewFailed"; requestId: string; message: string }
  | { tag: "edit" }
  | { tag: "submit" }
  | { tag: "submitted"; requestId: string; result: R }
  | { tag: "submitFailed"; requestId: string; message: string; definite: boolean }
  | { tag: "retry"; requestId: string }
  | { tag: "reset" };

export type PrepareCommand<F> =
  | { tag: "queryPreview"; requestId: string; form: F }
  | { tag: "cancelPreview"; requestId: string }
  | { tag: "submit"; requestId: string; form: F; hash: string };

export function stepPrepare<F, P extends { hash: string }, R>(
  state: PrepareState<F, P, R>,
  input: PrepareInput<F, P, R>,
): [PrepareState<F, P, R>, PrepareCommand<F>[]] {
  type S = PrepareState<F, P, R>;
  const same: [S, PrepareCommand<F>[]] = [state, []];
  const startPreview = (form: F, requestId: string, before: PrepareCommand<F>[] = []): [S, PrepareCommand<F>[]] => [
    { tag: "previewing", requestId, form },
    [...before, { tag: "queryPreview", requestId, form }],
  ];
  switch (input.tag) {
    case "preview":
      switch (state.tag) {
        case "editing":
        case "previewed":
        case "failed":
          return startPreview(input.form, input.requestId);
        case "previewing":
          return startPreview(input.form, input.requestId, [{ tag: "cancelPreview", requestId: state.requestId }]);
        case "submitting":
        case "created":
          return same;
      }
      return same;
    case "previewLoaded":
      if (state.tag !== "previewing" || state.requestId !== input.requestId) return same;
      return [{ tag: "previewed", requestId: state.requestId, form: state.form, preview: input.preview }, []];
    case "previewFailed":
      if (state.tag !== "previewing" || state.requestId !== input.requestId) return same;
      return [{ tag: "failed", message: input.message, retry: { tag: "preview", form: state.form } }, []];
    case "edit":
      if (state.tag === "previewed" || state.tag === "failed") return [{ tag: "editing" }, []];
      if (state.tag === "previewing") return [{ tag: "editing" }, [{ tag: "cancelPreview", requestId: state.requestId }]];
      return same;
    case "submit": {
      if (state.tag !== "previewed") return same;
      return [
        { tag: "submitting", requestId: state.requestId, form: state.form, preview: state.preview },
        [{ tag: "submit", requestId: state.requestId, form: state.form, hash: state.preview.hash }],
      ];
    }
    case "submitted":
      if (state.tag !== "submitting" || state.requestId !== input.requestId) return same;
      return [{ tag: "created", result: input.result, preview: state.preview }, []];
    case "submitFailed": {
      if (state.tag !== "submitting" || state.requestId !== input.requestId) return same;
      // A refusal (4xx) means nothing was created: start from a fresh preview. Anything else may
      // have reached the server, so the retry repeats the same request, which the server dedups.
      const retry: PrepareRetry<F, P> = input.definite
        ? { tag: "preview", form: state.form }
        : { tag: "submit", requestId: state.requestId, form: state.form, preview: state.preview };
      return [{ tag: "failed", message: input.message, retry }, []];
    }
    case "retry": {
      if (state.tag !== "failed") return same;
      const r = state.retry;
      if (r.tag === "submit") {
        return [
          { tag: "submitting", requestId: r.requestId, form: r.form, preview: r.preview },
          [{ tag: "submit", requestId: r.requestId, form: r.form, hash: r.preview.hash }],
        ];
      }
      return startPreview(r.form, input.requestId);
    }
    case "reset":
      return state.tag === "previewing"
        ? [{ tag: "editing" }, [{ tag: "cancelPreview", requestId: state.requestId }]]
        : [{ tag: "editing" }, []];
  }
}

// --- Retry confirmation (re-confirm the immutable spec hash; never automatic) -----------------

export type RetryState =
  | { tag: "closed" }
  | { tag: "reviewing"; hash: string; lines: string[] }
  | { tag: "sending"; hash: string; lines: string[] }
  | { tag: "failed"; hash: string; lines: string[]; message: string }
  | { tag: "missing" };

export type RetryInput =
  | { tag: "open"; spec: { hash: string; lines: string[] } | undefined }
  | { tag: "confirm" }
  | { tag: "succeeded" }
  | { tag: "failed"; message: string }
  | { tag: "close" };

export type RetryCommand = { tag: "sendRetry"; hash: string } | { tag: "refresh" };

export function stepRetry(state: RetryState, input: RetryInput): [RetryState, RetryCommand[]] {
  switch (input.tag) {
    case "open":
      if (state.tag === "sending") return [state, []];
      return [input.spec ? { tag: "reviewing", hash: input.spec.hash, lines: input.spec.lines } : { tag: "missing" }, []];
    case "confirm":
      if (state.tag !== "reviewing" && state.tag !== "failed") return [state, []];
      return [{ tag: "sending", hash: state.hash, lines: state.lines }, [{ tag: "sendRetry", hash: state.hash }]];
    case "succeeded":
      return state.tag === "sending" ? [{ tag: "closed" }, [{ tag: "refresh" }]] : [state, []];
    case "failed":
      return state.tag === "sending" ? [{ tag: "failed", hash: state.hash, lines: state.lines, message: input.message }, [{ tag: "refresh" }]] : [state, []];
    case "close":
      return state.tag === "sending" ? [state, []] : [{ tag: "closed" }, []];
  }
}

// --- Words for the backend lifecycle ------------------------------------------------------------

export const STAGE_LABEL: Record<string, string> = {
  create_directory: "Create the folder",
  init_git: "Start git",
  write_readme: "Write the README",
  write_ignore: "Write the ignore file",
  first_commit: "Make the first commit",
  finalize_tracking: "Start tracking the repo",
  create_remote: "Create the remote repository",
  add_origin: "Connect the remote",
  push_head: "Push the first commit",
};
export const stageLabel = (name: string): string => STAGE_LABEL[name] ?? name;

const PUBLISH_STAGES = new Set(["create_remote", "add_origin", "push_head"]);
export const isPublishStage = (name: string): boolean => PUBLISH_STAGES.has(name);

export const STAGE_STATUS_LABEL: Record<string, string> = {
  done: "Done",
  current: "In progress",
  pending: "Waiting",
  unknown: "Outcome unknown",
  failed: "Failed",
};

export type Tone = "waiting" | "running" | "done" | "unknown" | "problem";
export type NextStep =
  | { tag: "confirm"; proposalId: string }
  | { tag: "publish" }
  | { tag: "retry"; kind: "init" | "publish" }
  | { tag: "none" };

export interface OperationView {
  /** The server's lifecycle variant, kept so tests and rendering can see which one this is. */
  variant: string;
  headline: string;
  tone: Tone;
  detail: string;
  next: NextStep;
  /** Set once published (credential-free). */
  remoteUrl?: string;
}

const find = (op: NewRepoOperationDTO, status: string) => op.stages?.find((s) => s.status === status);
const retryKindOf = (op: NewRepoOperationDTO): "init" | "publish" => {
  const s = find(op, "unknown") ?? find(op, "failed");
  return s && isPublishStage(s.name) ? "publish" : "init";
};

/** Maps the backend lifecycle variant to what the person reads. Never derives a stage from flags. */
export function describeOperation(op: NewRepoOperationDTO): OperationView {
  const base = { variant: op.state, next: { tag: "none" } satisfies NextStep };
  switch (op.state) {
    case "reserved":
      return { ...base, headline: "Name reserved", tone: "waiting", detail: "The confirmation is being prepared. Nothing has been created on disk." };
    case "awaiting_confirm":
      return {
        ...base,
        headline: "Waiting for you",
        tone: "waiting",
        detail: "Nothing has been created on disk yet. Review the exact steps below. Confirming creates the folder, starts git, writes the files and makes the first commit.",
        next: op.proposalId ? { tag: "confirm", proposalId: op.proposalId } : { tag: "none" },
      };
    case "initializing":
      return { ...base, headline: "Setting up", tone: "running", detail: "Yggdrasil is creating the repo, one step at a time." };
    case "ready":
      return {
        ...base,
        headline: "Ready",
        tone: "done",
        detail: "The repo exists and is tracked. It has no remote yet; publish it to a host when you want a copy there.",
        next: { tag: "publish" },
      };
    case "publishing":
      return { ...base, headline: "Publishing", tone: "running", detail: "Yggdrasil is creating the remote repository and pushing the first commit." };
    case "published":
      return {
        ...base,
        headline: "Published",
        tone: "done",
        detail: "The repo has a remote and its first commit is pushed.",
        ...(op.remoteUrl ? { remoteUrl: op.remoteUrl } : {}),
      };
    case "uncertain": {
      const stage = find(op, "unknown");
      const what = stage ? stageLabel(stage.name).toLowerCase() : "the last step";
      const remote = stage && isPublishStage(stage.name) ? " A remote repository may or may not exist now." : "";
      return {
        ...base,
        headline: "Outcome unknown",
        tone: "unknown",
        detail: `Yggdrasil could not tell whether "${what}" finished.${remote} It is checking, read-only, and repeats nothing on its own. ${op.reason ?? ""}`.trim(),
        next: op.canRetry ? { tag: "retry", kind: retryKindOf(op) } : { tag: "none" },
      };
    }
    case "stopped": {
      const stage = find(op, "failed");
      const where = stage ? ` at "${stageLabel(stage.name).toLowerCase()}"` : "";
      return {
        ...base,
        headline: "Stopped",
        tone: "problem",
        detail: `Stopped${where}. Nothing was undone and nothing was deleted. ${op.reason ?? ""}`.trim(),
        next: op.canRetry ? { tag: "retry", kind: retryKindOf(op) } : { tag: "none" },
      };
    }
    default:
      return { ...base, headline: "Unknown state", tone: "problem", detail: "Yggdrasil reported a state this page does not know. Reload the page." };
  }
}

/** Whether the server's operation is still moving, so the page keeps reading it. */
export const isActive = (op: NewRepoOperationDTO): boolean => ["reserved", "initializing", "publishing", "uncertain"].includes(op.state);

// --- Forms --------------------------------------------------------------------------------------

export interface InitForm {
  leaf: string;
  readme: string;
  ignore: string;
  commitMessage: string;
}
export const emptyInitForm = (): InitForm => ({ leaf: "", readme: "", ignore: "", commitMessage: "" });

/** The preview request: empty optional text is left out so the server's defaults apply. */
export const initRequest = (f: InitForm) => ({
  leaf: f.leaf.trim(),
  ...(f.readme ? { readme: f.readme } : {}),
  ...(f.ignore ? { ignore: f.ignore } : {}),
  ...(f.commitMessage.trim() ? { commitMessage: f.commitMessage.trim() } : {}),
});

/** A leaf is one folder name: the server decides, this only avoids a request that cannot be valid. */
export const leafProblem = (leaf: string): string | undefined => {
  const t = leaf.trim();
  if (t === "") return "Enter a name for the new folder.";
  if (/[\\/]/.test(t)) return "Use a single folder name, not a path.";
  return undefined;
};

export interface PublishForm {
  hostInstance: string;
  owner: string;
  ownerKind: "user" | "org";
  name: string;
  visibility: "private" | "public";
}
