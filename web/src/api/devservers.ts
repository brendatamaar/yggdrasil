import { apiFetch } from "./client";
import type { DevServerActionRequest, DevServerDTO, DevServerLogMessage, DevServerLogResponse, DevServersResponse } from "./gen/api.gen";

const enc = encodeURIComponent;
const post: RequestInit = { method: "POST" };

/**
 * The generated DTO types the server ID as `any` (the Go type is core.DevServerID, which tygo
 * didn't resolve). This is the one place that narrows it to a string.
 */
export function serverId(s: { id: unknown }): string {
  return typeof s.id === "string" ? s.id : "";
}

export type ServerAction = "start" | "stop" | "restart";

export const fetchDevServers = async (): Promise<DevServerDTO[]> =>
  (await apiFetch<DevServersResponse>("/api/devservers")).servers;

/**
 * Every action names its target: a row that was never run is a placeholder with no entry on the
 * server, so the first start needs the repo, worktree and command (DevServerActionRequest).
 */
export const runServerAction = (server: DevServerDTO, action: ServerAction) =>
  apiFetch<DevServerDTO | undefined>(`/api/devservers/${enc(serverId(server))}/${action}`, {
    ...post,
    body: JSON.stringify({
      repoId: server.repoId,
      ...(server.worktreeId ? { worktreeId: server.worktreeId } : {}),
      command: server.command,
    } satisfies DevServerActionRequest),
  });

/**
 * The tail carries the same server-assigned seq as live lines. Defensively, a line without one
 * gets a negative number in order, so it sorts before every live line instead of colliding.
 */
export async function fetchServerLog(id: string, tail: number): Promise<DevServerLogResponse> {
  const r = await apiFetch<DevServerLogResponse>(`/api/devservers/${enc(id)}/log?tail=${String(tail)}`);
  const n = r.lines.length;
  return {
    ...r,
    lines: r.lines.map((l, i) => ((l as { seq?: unknown }).seq === undefined ? { ...l, seq: i - n } : l)),
  };
}

/** Reads a hub message's data as a DevServerChangedMessage's server; anything else is null. */
export function parseServerChanged(data: unknown): DevServerDTO | null {
  if (typeof data !== "object" || data === null) return null;
  const s = (data as { server?: unknown }).server;
  if (typeof s !== "object" || s === null) return null;
  const o = s as Record<string, unknown>;
  if (typeof o.id !== "string" || typeof o.status !== "string" || typeof o.repoId !== "string") return null;
  return s as DevServerDTO; // shape checked above: id, repoId and status are strings
}

/** Reads a hub message's data as one log line; anything else is null. */
export function parseServerLog(data: unknown): DevServerLogMessage | null {
  if (typeof data !== "object" || data === null) return null;
  const o = data as Record<string, unknown>;
  if (typeof o.id !== "string" || typeof o.seq !== "number" || typeof o.line !== "string" || typeof o.stream !== "string") return null;
  return { id: o.id, seq: o.seq, stream: o.stream, line: o.line };
}
