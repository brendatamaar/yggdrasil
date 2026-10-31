// Pure decisions of the trust UI (docs/AGENTS.md 5.11).
import type { TrustDTO } from "../../api/gen/api.gen";

// --- Trust flow ------------------------------------------------------------
// Trusting sends the config hash the person was shown. If the repo's config
// changes while the dialog is open, nothing is sent until they review again.

export type TrustFlow =
  | { tag: "idle" }
  | { tag: "reviewing"; hash: string }
  | { tag: "sending"; hash: string }
  | { tag: "changed" }
  | { tag: "failed"; message: string };

export type TrustInput =
  | { tag: "open"; hash: string }
  | { tag: "seen"; hash: string }
  | { tag: "confirm" }
  | { tag: "cancel" }
  | { tag: "succeeded" }
  | { tag: "failed"; message: string };

export type TrustCommand = { tag: "trust"; hash: string };

export function stepTrust(state: TrustFlow, input: TrustInput): [TrustFlow, TrustCommand[]] {
  switch (input.tag) {
    case "open":
      return state.tag === "sending" ? [state, []] : [{ tag: "reviewing", hash: input.hash }, []];
    case "seen":
      return state.tag === "reviewing" && state.hash !== input.hash ? [{ tag: "changed" }, []] : [state, []];
    case "confirm":
      return state.tag === "reviewing"
        ? [{ tag: "sending", hash: state.hash }, [{ tag: "trust", hash: state.hash }]]
        : [state, []];
    case "cancel":
      return state.tag === "sending" ? [state, []] : [{ tag: "idle" }, []];
    case "succeeded":
      return state.tag === "sending" ? [{ tag: "idle" }, []] : [state, []];
    case "failed":
      return state.tag === "sending" ? [{ tag: "failed", message: input.message }, []] : [state, []];
  }
}

// --- Display ---------------------------------------------------------------

export const WHY_TEXT: Record<string, string> = {
  never_trusted: "This repo ships agent settings you have not trusted yet.",
  config_changed: "The agent settings in this repo changed since you trusted them.",
  revoked: "You revoked trust for this repo.",
  scan_incomplete: "Yggdrasil could not read every agent settings file, so it cannot say what they would run.",
};

export const whyText = (why: string | undefined): string =>
  (why && WHY_TEXT[why]) || "This repo ships agent settings that need your trust first.";

/** A repo with no agent config needs no panel; a trusted one shows a quiet line. */
export const trustVisible = (t: Pick<TrustDTO, "state">): boolean => t.state !== "none";

/** The reason a task is in "Needs you", in plain words (SF-07, AG-11, AG-15). */
export function needsYouText(reason: string | undefined): { title: string; body: string } | null {
  switch (reason) {
    case "integrity_violated":
      return {
        title: "Ask mode changed your files",
        body: "This task was meant to only read, but its checkout changed. Yggdrasil stopped it. Look at the repo's changes before you do anything else.",
      };
    case "cap_exceeded":
      return {
        title: "This task hit its limit",
        body: "It reached its budget or turn limit and stopped. Raise the limit or start a new task to continue.",
      };
    case "pending_proposals":
      return { title: "Waiting for your review", body: "The agent proposed changes. Confirm or reject them below." };
    default:
      return null;
  }
}
