import { describeError } from "../../api/client";
import { HostContextDecodeError } from "./api";
import type { HostContextReason, HostContextStatusDTO } from "../../api/gen/api.gen";

// Pure reading of the shared status of every host context answer (docs/design/phase-6-host-context.md).
// A blocked or partial read is a normal answer; this says it in plain language.

export type StatusView =
  | { tag: "ready" }
  | { tag: "partial"; text: string; gaps: string[] }
  | { tag: "waiting"; text: string }
  | { tag: "blocked"; reason: HostContextReason; text: string; retryable: boolean };

const BLOCKED_TEXT: Record<string, string> = {
  no_token: "Add a token for this host in Settings to read this. Local features still work.",
  forbidden: "The host refused the token. Check that it can read this project.",
  not_found: "The host can't find this item. It may have been removed, or its head moved.",
  unsupported: "This host isn't supported for this view yet.",
  retry_exhausted: "The host isn't answering right now.",
  untracked: "This repo isn't tracked any more.",
  too_large: "This is too large to show here.",
};

const GAP_TEXT: Record<string, string> = {
  no_token: "no token",
  forbidden: "not allowed",
  not_found: "not found",
  unsupported: "unsupported",
  retry_exhausted: "host not answering",
  untracked: "untracked",
  too_large: "too large",
};

const at = (iso: string | undefined): string => {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
};

export function describeStatus(s: HostContextStatusDTO): StatusView {
  switch (s.phase) {
    case "ready":
      return { tag: "ready" };
    case "partial": {
      const gaps = (s.unavailable ?? []).map((u) => `${u.source}: ${GAP_TEXT[u.reason] ?? u.reason}`);
      return { tag: "partial", text: s.message || "Some parts could not be read. What is shown may be incomplete.", gaps };
    }
    case "waiting": {
      const when = at(s.retryAt);
      return {
        tag: "waiting",
        text: s.message || `The host is busy or limiting requests.${when ? ` Trying again at ${when}.` : " Trying again shortly."}`,
      };
    }
    case "blocked": {
      const reason = s.reason ?? "unsupported";
      const base = s.message || BLOCKED_TEXT[reason] || "This could not be read.";
      // A rate limit or an exhausted retry carries the time it is worth trying again.
      const until = reason === "retry_exhausted" || s.expiresAt ? at(s.expiresAt ?? s.retryAt) : "";
      return {
        tag: "blocked",
        reason,
        text: until ? `${base} Try again after ${until}.` : base,
        retryable: reason !== "no_token" && reason !== "unsupported" && reason !== "untracked",
      };
    }
    default:
      // An unknown phase is not a success: say so instead of showing nothing.
      return { tag: "blocked", reason: "unsupported", text: "The server sent a status this version does not know.", retryable: false };
  }
}

/**
 * A read that failed with a transport or server error (not a normal blocked answer). A read
 * overtaken by a newer change comes back as 503 "unavailable" with a plain "try again" message
 * (the server's TryAgain); the Try again button next to the text is the retry hint.
 */
export function readErrorText(err: unknown): { text: string } {
  if (err instanceof HostContextDecodeError) return { text: err.message };
  return { text: describeError(err) };
}
