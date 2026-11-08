// The terminal connection as a pure reducer: (state, input) -> (state, commands).
// The raw socket token only ever travels inside the `tokenIssued` input and the
// `openSocket` command. It is never part of the state, so nothing can render,
// store or log it by accident (docs/design/terminal.md).

export type FailureKind = "expired" | "too_many" | "not_tracked" | "other";

export type TermState =
  | { tag: "idle" }
  | { tag: "requesting" }
  | { tag: "connecting" }
  | { tag: "open" }
  | { tag: "failed"; kind: FailureKind; message: string }
  | { tag: "ended" };

export type TermInput =
  | { tag: "connect" }
  | { tag: "tokenIssued"; token: string }
  | { tag: "tokenRejected"; kind: FailureKind; message: string }
  | { tag: "socketOpened" }
  | { tag: "socketClosed" }
  | { tag: "socketFailed" }
  | { tag: "close" };

export type TermCommand =
  | { tag: "requestToken" }
  | { tag: "openSocket"; token: string }
  | { tag: "sendSize" }
  | { tag: "closeSocket" };

export interface TermStep {
  state: TermState;
  commands: TermCommand[];
}

export const initialTermState: TermState = { tag: "idle" };

const none = (state: TermState): TermStep => ({ state, commands: [] });

export const failureMessages: Record<Exclude<FailureKind, "other">, string> = {
  expired: "The terminal link expired before it could be used. Reconnect to get a new one.",
  too_many: "Too many terminals are open. Close one, then reconnect.",
  not_tracked: "This folder is no longer tracked by Yggdrasil, so a terminal can't open in it.",
};

/** One step. An input the current state doesn't handle changes nothing (docs/AGENTS.md 5.11). */
export function termStep(state: TermState, input: TermInput): TermStep {
  switch (input.tag) {
    case "connect":
      // Every connect, a reconnect included, asks for a new token.
      if (state.tag === "idle" || state.tag === "failed" || state.tag === "ended") {
        return { state: { tag: "requesting" }, commands: [{ tag: "requestToken" }] };
      }
      return none(state);
    case "tokenIssued":
      return state.tag === "requesting"
        ? { state: { tag: "connecting" }, commands: [{ tag: "openSocket", token: input.token }] }
        : none(state);
    case "tokenRejected":
      return state.tag === "requesting" ? none({ tag: "failed", kind: input.kind, message: input.message }) : none(state);
    case "socketOpened":
      return state.tag === "connecting" ? { state: { tag: "open" }, commands: [{ tag: "sendSize" }] } : none(state);
    case "socketClosed":
      // Closed before it opened: the token was refused or had expired. Closed after: the shell ended.
      if (state.tag === "connecting") return none({ tag: "failed", kind: "expired", message: failureMessages.expired });
      if (state.tag === "open") return none({ tag: "ended" });
      return none(state);
    case "socketFailed":
      if (state.tag === "connecting" || state.tag === "open") {
        return none({
          tag: "failed",
          kind: "other",
          message: "The terminal connection failed. Check that Yggdrasil is running, then reconnect.",
        });
      }
      return none(state);
    case "close":
      return state.tag === "idle" ? none(state) : { state: { tag: "idle" }, commands: [{ tag: "closeSocket" }] };
  }
}

/** Sorts a failed token request into the cases the dock explains in its own words. */
export function classifyTokenFailure(status: number, code: string): FailureKind {
  if (status === 404 || status === 403 || code === "not_found" || code === "not_tracked") return "not_tracked";
  if (status === 429 || code.includes("too_many") || code.includes("limit")) return "too_many";
  if (status === 410 || code === "expired") return "expired";
  return "other";
}
