import { apiFetch } from "./client";
import type { TerminalResizeFrame, TerminalTokenRequest, TerminalTokenResponse } from "./gen/api.gen";

/** Asks for a new single-use socket token. Every connect, reconnects included, calls this again. */
export const requestTerminalToken = (target: TerminalTokenRequest) =>
  apiFetch<TerminalTokenResponse>("/api/terminal", { method: "POST", body: JSON.stringify(target) });

export function terminalSocketUrl(token: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}/ws/terminal?token=${encodeURIComponent(token)}`;
}

export const resizeFrame = (cols: number, rows: number): string =>
  JSON.stringify({ type: "resize", cols, rows } satisfies TerminalResizeFrame);
