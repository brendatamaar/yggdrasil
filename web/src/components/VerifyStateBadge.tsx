export interface BadgeInfo {
  label: string;
  tone: "ok" | "danger" | "warn" | "muted" | "busy";
}

const badges: Record<string, BadgeInfo> = {
  none: { label: "Not verified", tone: "muted" },
  skipped: { label: "Skipped", tone: "muted" },
  unverifiable: { label: "Can't verify", tone: "warn" },
  bootstrapping: { label: "Setting up", tone: "busy" },
  verifying: { label: "Verifying", tone: "busy" },
  passed: { label: "Passed", tone: "ok" },
  failed: { label: "Failed", tone: "danger" },
  interrupted: { label: "Interrupted", tone: "warn" },
  cancelled: { label: "Cancelled", tone: "muted" },
};

/** Label and tone of a verification state; an unknown state shows as itself. */
export function badgeFor(state: string): BadgeInfo {
  return badges[state] ?? { label: state, tone: "muted" };
}

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
