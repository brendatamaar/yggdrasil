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

