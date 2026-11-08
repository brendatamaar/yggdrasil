import { apiFetch } from "./client";
import type { DevServerDTO, DevServerLogMessage, DevServerLogResponse, DevServersResponse } from "./gen/api.gen";

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

export const runServerAction = (id: string, action: ServerAction) =>
  apiFetch<DevServerDTO | undefined>(`/api/devservers/${enc(id)}/${action}`, post);

export const fetchServerLog = (id: string, tail: number): Promise<DevServerLogResponse> =>
  apiFetch<DevServerLogResponse>(`/api/devservers/${enc(id)}/log?tail=${String(tail)}`);

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
