import type { BuildDTO, BuildStepDTO } from "../../api/gen/api.gen";

export const input = "rounded-md border border-line bg-canvas px-2 py-1 text-sm";

export function buildLabel(b: Pick<BuildDTO, "status" | "cancelledAt" | "finishedAt">): string {
  if (b.status === "running" && b.cancelledAt && !b.finishedAt) return "Cancelling";
  const s = b.status;
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export const statusTone = (status: string): string =>
  status === "succeeded"
    ? "text-green-500"
    : status === "failed"
      ? "text-red-500"
      : status === "running"
        ? "text-accent"
        : "text-muted";

export const isActive = (b: Pick<BuildDTO, "status">) => b.status === "queued" || b.status === "running";

export function formatDuration(startIso?: string, endIso?: string, now = Date.now()): string {
  if (!startIso) return "-";
  const end = endIso ? new Date(endIso).getTime() : now;
  const s = Math.max(0, Math.round((end - new Date(startIso).getTime()) / 1000));
  if (s < 60) return `${String(s)}s`;
  return `${String(Math.floor(s / 60))}m ${String(s % 60)}s`;
}

export function formatSize(n: number): string {
  if (n < 1024) return `${String(n)} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

const startFailureText: Record<string, string> = {
  not_found: "The program wasn't found. Check that it is installed and on the PATH.",
  bad_args: "The arguments were not accepted.",
  dir_missing: "The folder to run in doesn't exist.",
};

/** Plain-language outcome of a step. */
export function stepOutcomeText(s: BuildStepDTO): string {
  switch (s.outcome) {
    case "succeeded":
      return "Succeeded";
    case "failed":
      return s.exitCode === undefined ? "Failed" : `Failed with exit code ${String(s.exitCode)}`;
    case "start_failed":
      return `Could not start. ${s.startMessage ?? startFailureText[s.startFailure ?? ""] ?? "Check the command and try again."}`;
    case "cancelled":
      return "Cancelled";
    case "interrupted":
      return "Interrupted when Yggdrasil stopped";
    default:
      return s.status === "skipped" ? "Skipped" : s.status === "running" ? "Running" : "Waiting";
  }
}

/** Splits a command into arguments. Quotes group words; there is no shell syntax. */
export function splitArgs(cmd: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quote = "";
  let has = false;
  for (const ch of cmd) {
    if (quote) {
      if (ch === quote) quote = "";
      else cur += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      has = true;
    } else if (/\s/.test(ch)) {
      if (has || cur) out.push(cur);
      cur = "";
      has = false;
    } else cur += ch;
  }
  if (has || cur) out.push(cur);
  return out;
}

export const joinArgs = (argv: string[]): string =>
  argv.map((a) => (a === "" || /[\s"']/.test(a) ? `"${a}"` : a)).join(" ");
