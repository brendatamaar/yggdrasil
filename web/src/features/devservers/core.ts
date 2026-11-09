import {
  DevServerFailed,
  DevServerOrphaned,
  DevServerRunning,
  DevServerStarting,
  DevServerStopped,
  DevServerStopping,
  type DevServerDTO,
  type DevServerLogMessage,
} from "../../api/gen/api.gen";
import { serverId } from "../../api/devservers";

// Pure helpers for dev servers: labels, which buttons apply, log merging and the
// auto-scroll state. No IO here; the hooks and components run the effects.

export const LOG_TAIL = 200;
/** The most lines kept on screen. Older ones are dropped from the top. */
export const LOG_CAP = 2000;

const labels: Record<string, string> = {
  [DevServerStopped]: "Stopped",
  [DevServerStarting]: "Starting",
  [DevServerRunning]: "Running",
  [DevServerStopping]: "Stopping",
  [DevServerFailed]: "Failed",
  [DevServerOrphaned]: "Orphaned",
};

export const statusLabel = (status: string): string => labels[status] ?? "Unknown";

export function statusTone(status: string): string {
  if (status === DevServerRunning) return "text-green-500";
  if (status === DevServerFailed || status === DevServerOrphaned) return "text-danger";
  if (status === DevServerStarting || status === DevServerStopping) return "text-accent";
  return "text-muted";
}

/** Which buttons a server in this state offers. An orphan can only be stopped. */
export interface ServerButtons {
  start: boolean;
  stop: boolean;
  restart: boolean;
}

export function serverButtons(status: string): ServerButtons {
  switch (status) {
    case DevServerStopped:
    case DevServerFailed:
      return { start: true, stop: false, restart: false };
    case DevServerRunning:
      return { start: false, stop: true, restart: true };
    case DevServerStopping: // Stop again escalates the kill
      return { start: false, stop: true, restart: false };
    case DevServerOrphaned:
      return { start: false, stop: true, restart: false };
    default: // starting or unknown: wait for the next state
      return { start: false, stop: false, restart: false };
  }
}

export const portHref = (port: number): string => `http://127.0.0.1:${String(port)}`;

const rank = (s: string): number =>
  s === DevServerOrphaned ? 0 : s === DevServerFailed ? 1 : s === DevServerRunning || s === DevServerStarting ? 2 : 3;

/** Orphaned first (the person has to act), then failed, running, the rest; stable inside a group. */
export function sortServers(servers: readonly DevServerDTO[]): DevServerDTO[] {
  return servers
    .map((s, i) => ({ s, i }))
    .sort((a, b) => rank(a.s.status) - rank(b.s.status) || a.i - b.i)
    .map(({ s }) => s);
}

/** Replaces the server with the same ID, or adds it. */
export function upsertServer(list: readonly DevServerDTO[] | undefined, next: DevServerDTO): DevServerDTO[] {
  const id = serverId(next);
  const base = list ?? [];
  return base.some((s) => serverId(s) === id) ? base.map((s) => (serverId(s) === id ? next : s)) : [...base, next];
}

/** A line is identified by (epoch, seq): seq restarts at 1 when Yggdrasil restarts. */
export const lineKey = (l: Pick<DevServerLogMessage, "epoch" | "seq">): string => `${String(l.epoch)}:${String(l.seq)}`;

/** Merges log lines by (epoch, seq): no duplicates, in order, capped to the newest LOG_CAP. */
export function mergeLog(
  current: readonly DevServerLogMessage[],
  incoming: readonly DevServerLogMessage[],
  cap = LOG_CAP,
): DevServerLogMessage[] {
  if (incoming.length === 0) return current as DevServerLogMessage[];
  const byKey = new Map<string, DevServerLogMessage>();
  for (const l of current) byKey.set(lineKey(l), l);
  for (const l of incoming) byKey.set(lineKey(l), l);
  const merged = [...byKey.values()].sort((a, b) => a.epoch - b.epoch || a.seq - b.seq);
  return merged.length > cap ? merged.slice(merged.length - cap) : merged;
}

// Auto-scroll: following the end of the log, or paused because the person scrolled up.
export type Follow = { tag: "following" } | { tag: "paused" };
export type FollowInput = { tag: "scrolled"; atBottom: boolean } | { tag: "resume" };

export function followStep(state: Follow, input: FollowInput): Follow {
  if (input.tag === "resume") return state.tag === "following" ? state : { tag: "following" };
  if (input.atBottom) return state.tag === "following" ? state : { tag: "following" };
  return state.tag === "paused" ? state : { tag: "paused" };
}
