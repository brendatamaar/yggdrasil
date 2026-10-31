import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "../../api/client";
import type { AgentDTO, AgentsResponse } from "../../api/gen/api.gen";
import { useHealth } from "../../api/queries";

export interface AgentChoice {
  id: string;
  label: string;
  available: boolean;
  /** Why it cannot be picked. */
  reason?: string;
}

/** The server's agent list as choices (pure). */
export function toChoices(agents: readonly AgentDTO[]): AgentChoice[] {
  return agents.map((a) => ({
    id: a.id,
    label: a.label,
    available: a.available,
    ...(a.reason ? { reason: a.reason } : {}),
  }));
}

/** The agent to use: the wanted one if the server offers it and it is available, else the first available one, else "". */
export function pickAgent(choices: readonly AgentChoice[], wanted: string): string {
  const w = choices.find((c) => c.id === wanted);
  if (w?.available) return w.id;
  return choices.find((c) => c.available)?.id ?? "";
}

/** Write modes are off only when the server says so; while unknown they are not hidden. */
export const writeModesOffFromHealth = (health: { writeModes: boolean } | undefined): boolean =>
  health !== undefined && !health.writeModes;

export const agentsKey = ["agents"] as const;

export function useAgents() {
  return useQuery({
    queryKey: agentsKey,
    queryFn: async () => toChoices((await apiFetch<AgentsResponse>("/api/agents")).agents),
  });
}

/** True when the server (GET /api/health) has write modes off. */
export function useWriteModesOff(): boolean {
  const health = useHealth();
  return writeModesOffFromHealth(health.data);
}
