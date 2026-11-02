// Pure decisions of the change-set UI (docs/AGENTS.md 5.11): no IO, no clock.
import type { ChangeSetDTO, ConfirmRunRequest, ScopeDTO, WarningDTO } from "../../api/gen/api.gen";

// --- Merge order -----------------------------------------------------------

/** Moves the repo at `index` one place up or down. Out of range or at an edge: unchanged. Always a permutation. */
export function moveRepo(order: readonly string[], index: number, dir: -1 | 1): string[] {
  const to = index + dir;
  if (index < 0 || index >= order.length || to < 0 || to >= order.length) return [...order];
  const next = [...order];
  const a = next[index];
  const b = next[to];
  if (a === undefined || b === undefined) return next;
  next[index] = b;
  next[to] = a;
  return next;
}

export const sameOrder = (a: readonly string[], b: readonly string[]) => a.length === b.length && a.every((x, i) => x === b[i]);

// --- Members in scope ------------------------------------------------------

/** Landing proposals are confirmed one by one from the proposals list, never as part of a run. */
const landingKinds = new Set(["merge_local", "remove_worktree"]);
export const isLanding = (kind: string) => landingKinds.has(kind);

export interface RunItem {
  proposalId: string;
  repoId: string;
  kind: string;
  hash: string;
}

/** The pending members a run of `scope` would confirm, in merge order. */
export function runItems(cs: Pick<ChangeSetDTO, "members" | "order">, scope: ScopeDTO): RunItem[] {
  const rank = (repo: string) => {
    const i = cs.order.indexOf(repo);
    return i < 0 ? cs.order.length : i;
  };
  return cs.members
    .filter((m) => m.status === "pending" && !isLanding(m.kind) && (scope.kind === "all" || m.repoId === scope.repoId))
    .map((m) => ({ proposalId: m.proposalId, repoId: m.repoId, kind: m.kind, hash: m.hash }))
    .sort((a, b) => rank(a.repoId) - rank(b.repoId));
}

/** Distinct warning codes: one acknowledgement covers all warnings with that code. */
export const warningCodes = (ws: readonly WarningDTO[]) => [...new Set(ws.map((w) => w.code))];

// --- Confirm-run flow ------------------------------------------------------
// idle -> loading (preflight) -> reviewing (items + warnings + acks) -> sending -> idle.
// The request carries exactly the hashes shown. If the set changes while the
// dialog is open the flow goes to "changed" and nothing is sent.

export type RunFlow =
  | { tag: "idle" }
  | { tag: "loading"; scope: ScopeDTO; items: RunItem[] }
  | { tag: "preflightFailed"; scope: ScopeDTO; items: RunItem[]; message: string }
  | { tag: "reviewing"; scope: ScopeDTO; items: RunItem[]; warnings: WarningDTO[]; acked: string[]; error?: string }
  | { tag: "sending"; scope: ScopeDTO; items: RunItem[]; warnings: WarningDTO[]; acked: string[] }
  | { tag: "changed"; scope: ScopeDTO };

export type RunInput =
  | { tag: "open"; scope: ScopeDTO; items: RunItem[] }
  | { tag: "preflighted"; warnings: WarningDTO[] }
  | { tag: "preflightFailed"; message: string }
  | { tag: "retry" }
  | { tag: "ack"; code: string; on: boolean }
  | { tag: "seen"; items: RunItem[] }
  | { tag: "submit" }
  | { tag: "accepted" }
  | { tag: "rejected"; conflict: boolean; message: string }
  | { tag: "cancel" };

export type RunCommand = { tag: "preflight"; scope: ScopeDTO } | { tag: "confirm"; request: ConfirmRunRequest };

export const canSubmit = (s: RunFlow): boolean =>
  s.tag === "reviewing" && s.items.length > 0 && warningCodes(s.warnings).every((c) => s.acked.includes(c));

const sameItems = (a: RunItem[], b: RunItem[]) =>
  a.length === b.length && a.every((x, i) => {
    const y = b[i];
    return y !== undefined && x.proposalId === y.proposalId && x.hash === y.hash;
  });

export function stepRun(s: RunFlow, i: RunInput): [RunFlow, RunCommand[]] {
  switch (i.tag) {
    case "open":
      if (s.tag === "sending") return [s, []];
      return [{ tag: "loading", scope: i.scope, items: i.items }, [{ tag: "preflight", scope: i.scope }]];
    case "preflighted":
      if (s.tag !== "loading") return [s, []];
      return [{ tag: "reviewing", scope: s.scope, items: s.items, warnings: i.warnings, acked: [] }, []];
    case "preflightFailed":
      if (s.tag !== "loading") return [s, []];
      // Without the warnings nothing can be confirmed: only a retry is offered.
      return [{ tag: "preflightFailed", scope: s.scope, items: s.items, message: i.message }, []];
    case "retry":
      if (s.tag !== "preflightFailed") return [s, []];
      return [{ tag: "loading", scope: s.scope, items: s.items }, [{ tag: "preflight", scope: s.scope }]];
    case "ack": {
      if (s.tag !== "reviewing") return [s, []];
      const rest = s.acked.filter((c) => c !== i.code);
      return [{ ...s, acked: i.on ? [...rest, i.code] : rest }, []];
    }
    case "seen":
      if (s.tag !== "reviewing" && s.tag !== "loading" && s.tag !== "preflightFailed") return [s, []];
      return sameItems(s.items, i.items) ? [s, []] : [{ tag: "changed", scope: s.scope }, []];
    case "submit": {
      if (s.tag !== "reviewing" || !canSubmit(s)) return [s, []];
      const seen: Record<string, string> = {};
      for (const it of s.items) seen[it.proposalId] = it.hash;
      const request: ConfirmRunRequest = { scope: s.scope, seen, ...(s.acked.length > 0 ? { ack: [...s.acked] } : {}) };
      return [{ tag: "sending", scope: s.scope, items: s.items, warnings: s.warnings, acked: s.acked }, [{ tag: "confirm", request }]];
    }
    case "accepted":
      return s.tag === "sending" ? [{ tag: "idle" }, []] : [s, []];
    case "rejected":
      if (s.tag !== "sending") return [s, []];
      return i.conflict
        ? [{ tag: "changed", scope: s.scope }, []]
        : [{ tag: "reviewing", scope: s.scope, items: s.items, warnings: s.warnings, acked: s.acked, error: i.message }, []];
    case "cancel":
      return s.tag === "sending" ? [s, []] : [{ tag: "idle" }, []];
  }
}

// --- Landing dialogs (merge into main checkout, remove worktree) -----------

export type LandingKind = "merge_local" | "remove_worktree";

export type LandingFlow =
  | { tag: "closed" }
  | { tag: "form"; kind: LandingKind; worktreeId: string; title: string; error?: string }
  | { tag: "sending"; kind: LandingKind; worktreeId: string; title: string }
  | { tag: "proposed"; kind: LandingKind; worktreeId: string; proposalId: string };

export type LandingInput =
  | { tag: "open"; kind: LandingKind; worktreeId: string; title: string }
  | { tag: "type"; title: string }
  | { tag: "submit" }
  | { tag: "created"; proposalId: string }
  | { tag: "failed"; message: string }
  | { tag: "close" };

/** Only ever a request to CREATE a proposal; confirming is a different flow. */
export type LandingCommand = { tag: "propose"; kind: LandingKind; worktreeId: string; title?: string };

export function stepLanding(s: LandingFlow, i: LandingInput): [LandingFlow, LandingCommand[]] {
  switch (i.tag) {
    case "open":
      if (s.tag === "sending") return [s, []];
      return [{ tag: "form", kind: i.kind, worktreeId: i.worktreeId, title: i.title }, []];
    case "type":
      return s.tag === "form" ? [{ ...s, title: i.title, error: undefined }, []] : [s, []];
    case "submit": {
      if (s.tag !== "form") return [s, []];
      const title = s.title.trim();
      const cmd: LandingCommand = { tag: "propose", kind: s.kind, worktreeId: s.worktreeId, ...(s.kind === "merge_local" && title ? { title } : {}) };
      return [{ tag: "sending", kind: s.kind, worktreeId: s.worktreeId, title: s.title }, [cmd]];
    }
    case "created":
      return s.tag === "sending" ? [{ tag: "proposed", kind: s.kind, worktreeId: s.worktreeId, proposalId: i.proposalId }, []] : [s, []];
    case "failed":
      return s.tag === "sending" ? [{ tag: "form", kind: s.kind, worktreeId: s.worktreeId, title: s.title, error: i.message }, []] : [s, []];
    case "close":
      return s.tag === "sending" ? [s, []] : [{ tag: "closed" }, []];
  }
}

// --- Display ---------------------------------------------------------------

export function runSummary(
  cs: Pick<ChangeSetDTO, "run" | "confirming">,
  nameOf: (proposalId: string) => string | undefined,
): string | undefined {
  if (cs.confirming) return `Confirming… ${String(cs.confirming.remaining)} left.`;
  if (cs.run.state === "halted") {
    const at = cs.run.at ? nameOf(cs.run.at) : undefined;
    return `Stopped${at ? ` at ${at}` : ""}: ${cs.run.reason || "a proposal did not complete"}. The remaining proposals were not confirmed.`;
  }
  if (cs.run.state === "completed") return "All proposals were confirmed.";
  return undefined;
}
