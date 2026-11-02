import { badgeFor } from "./verifyBadge";

const tone: Record<string, string> = {
  ok: "border-ok text-ok",
  danger: "border-danger text-danger",
  warn: "border-warn text-warn",
  muted: "border-line text-muted",
  busy: "border-accent text-accent",
};

/** One verification state as a badge. Shared by the Review tab and the board card. */
export function VerifyStateBadge({ state, suffix = "" }: { state: string; suffix?: string }) {
  const b = badgeFor(state);
  return (
    <span className={`rounded-full border px-2 py-0.5 text-xs ${tone[b.tone] ?? ""}`} data-state={state}>
      {b.label}
      {suffix}
    </span>
  );
}
