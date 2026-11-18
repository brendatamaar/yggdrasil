// Pure decisions of the workspace-action dialog (docs/design/phase-6-ui.md,
// docs/AGENTS.md 5.11): (state, input) -> (state, commands). No IO, no clock,
// no random values: the RequestID arrives inside the Open input.
import type { ActionKind, ActionPreviewDTO, ActionRequestDTO, ActionResultDTO } from "../../api/gen/api.gen";

/** What the person picked: a scoped, catalogue-offered action. Never a URL or a path. */
export interface ActionTarget {
  repoId: string;
  kind: ActionKind;
  /** Pins the object the action acts on (CI run, MR number, build ID, issue number). */
  sourceId?: string;
  agent?: string;
  model?: string;
  /** Plain-language title of the offering, for the dialog. */
  title: string;
  /** Display only: the repo's name, shown as the scope. */
  repoName?: string;
}

/** A submission that was sent and may still be running on the server. */
export interface InFlight {
  target: ActionTarget;
  requestId: string;
  digest: string;
  expectedHead?: string;
}

/** What a retry does: preview again (a new request) or send the same submission again (same RequestID, idempotent on the server). */
export type RetryContext = { tag: "preview"; target: ActionTarget } | ({ tag: "submit" } & InFlight);

export type ActionState =
  | { tag: "closed"; inFlight?: InFlight }
  | { tag: "loading"; requestId: string; target: ActionTarget }
  | { tag: "preview"; requestId: string; target: ActionTarget; preview: ActionPreviewDTO }
  | ({ tag: "submitting" } & InFlight)
  | { tag: "succeeded"; requestId: string; target: ActionTarget; result: ActionResultDTO }
  | { tag: "failed"; message: string; retry: RetryContext };

export type ActionInput =
  | { tag: "open"; target: ActionTarget; requestId: string }
  | { tag: "loaded"; requestId: string; preview: ActionPreviewDTO }
  | { tag: "loadFailed"; requestId: string; message: string }
  | { tag: "submit" }
  | { tag: "submitted"; requestId: string; result: ActionResultDTO }
  | { tag: "submitFailed"; requestId: string; message: string; definite: boolean }
  | { tag: "retry"; requestId: string }
  | { tag: "close" };

export type ActionCommand =
  | { tag: "queryPreview"; request: ActionRequestDTO }
  | { tag: "cancelQuery"; requestId: string }
  | { tag: "submitAction"; request: ActionRequestDTO; digest: string; expectedHead?: string }
  | { tag: "reconcile"; requestId: string }
  | { tag: "invalidate" };

type Step = [ActionState, ActionCommand[]];

export function requestFor(target: ActionTarget, requestId: string): ActionRequestDTO {
  return {
    requestId,
    repoId: target.repoId,
    kind: target.kind,
    ...(target.sourceId ? { sourceId: target.sourceId } : {}),
    ...(target.agent ? { agent: target.agent } : {}),
    ...(target.model ? { model: target.model } : {}),
  };
}

const sameTarget = (a: ActionTarget, b: ActionTarget): boolean =>
  a.repoId === b.repoId && a.kind === b.kind && (a.sourceId ?? "") === (b.sourceId ?? "");

const inFlightOf = (f: InFlight): InFlight => ({
  target: f.target,
  requestId: f.requestId,
  digest: f.digest,
  ...(f.expectedHead ? { expectedHead: f.expectedHead } : {}),
});

const submitCommand = (f: InFlight): ActionCommand => ({
  tag: "submitAction",
  request: requestFor(f.target, f.requestId),
  digest: f.digest,
  ...(f.expectedHead ? { expectedHead: f.expectedHead } : {}),
});

function startPreview(target: ActionTarget, requestId: string, before: ActionCommand[] = []): Step {
  return [
    { tag: "loading", requestId, target },
    [...before, { tag: "queryPreview", request: requestFor(target, requestId) }],
  ];
}

/** A server answer for a request: completed is done, refused or cancelled is a plain failure, submitting is still running. */
function settle(s: Extract<ActionState, { tag: "submitting" }>, result: ActionResultDTO): Step {
  switch (result.state) {
    case "completed":
      return [{ tag: "succeeded", requestId: s.requestId, target: s.target, result }, [{ tag: "invalidate" }]];
    case "submitting":
      return [s, [{ tag: "reconcile", requestId: s.requestId }]];
    default:
      return [
        { tag: "failed", message: result.message || "The action was not started.", retry: { tag: "preview", target: s.target } },
        [{ tag: "invalidate" }],
      ];
  }
}

export function stepAction(state: ActionState, input: ActionInput): Step {
  const same: Step = [state, []];
  switch (input.tag) {
    case "open": {
      switch (state.tag) {
        case "closed": {
          // A submission of this action is still out there: reconcile it, never send a second one.
          const f = state.inFlight;
          if (f && sameTarget(f.target, input.target)) return [{ tag: "submitting", ...inFlightOf(f) }, [submitCommand(f)]];
          return startPreview(input.target, input.requestId);
        }
        case "loading":
          return startPreview(input.target, input.requestId, [{ tag: "cancelQuery", requestId: state.requestId }]);
        case "preview":
        case "failed":
        case "succeeded":
          return startPreview(input.target, input.requestId);
        case "submitting":
          return same;
      }
      return same;
    }
    case "loaded": {
      if (state.tag !== "loading" || state.requestId !== input.requestId) return same;
      if (input.preview.requestId !== state.requestId) {
        return [
          { tag: "failed", message: "The server answered a different request. Try again.", retry: { tag: "preview", target: state.target } },
          [],
        ];
      }
      return [{ tag: "preview", requestId: state.requestId, target: state.target, preview: input.preview }, []];
    }
    case "loadFailed": {
      if (state.tag !== "loading" || state.requestId !== input.requestId) return same;
      return [{ tag: "failed", message: input.message, retry: { tag: "preview", target: state.target } }, []];
    }
    case "submit": {
      if (state.tag !== "preview") return same;
      const f: InFlight = {
        target: state.target,
        requestId: state.requestId,
        digest: state.preview.digest,
        ...(state.preview.expectedHead ? { expectedHead: state.preview.expectedHead } : {}),
      };
      return [{ tag: "submitting", ...f }, [submitCommand(f)]];
    }
    case "submitted": {
      if (state.tag === "closed" && state.inFlight?.requestId === input.requestId) {
        // Closed while it ran: refresh. An answer of "still running" keeps the key.
        if (input.result.state === "submitting") return [state, [{ tag: "reconcile", requestId: input.requestId }]];
        return [{ tag: "closed" }, [{ tag: "invalidate" }]];
      }
      if (state.tag !== "submitting" || state.requestId !== input.requestId) return same;
      return settle(state, input.result);
    }
    case "submitFailed": {
      if (state.tag === "closed" && state.inFlight?.requestId === input.requestId) {
        // A definite refusal frees the key; an unknown outcome keeps it so a reopen reconciles.
        return input.definite ? [{ tag: "closed" }, []] : same;
      }
      if (state.tag !== "submitting" || state.requestId !== input.requestId) return same;
      if (input.definite) {
        return [{ tag: "failed", message: input.message, retry: { tag: "preview", target: state.target } }, []];
      }
      return [{ tag: "failed", message: input.message, retry: { tag: "submit", ...inFlightOf(state) } }, []];
    }
    case "retry": {
      if (state.tag !== "failed") return same;
      const r = state.retry;
      if (r.tag === "submit") return [{ tag: "submitting", ...inFlightOf(r) }, [submitCommand(r)]];
      return startPreview(r.target, input.requestId);
    }
    case "close": {
      switch (state.tag) {
        case "closed":
          return same;
        case "loading":
          return [{ tag: "closed" }, [{ tag: "cancelQuery", requestId: state.requestId }]];
        case "submitting":
          // Closing never cancels a submitted request; the key is kept so a reopen reconciles.
          return [{ tag: "closed", inFlight: inFlightOf(state) }, []];
        case "failed":
          if (state.retry.tag === "submit") return [{ tag: "closed", inFlight: inFlightOf(state.retry) }, []];
          return [{ tag: "closed" }, []];
        default:
          return [{ tag: "closed" }, []];
      }
    }
  }
}

/** Whether the dialog shows anything. */
export const isOpen = (s: ActionState): boolean => s.tag !== "closed";
