// Pure decisions of the proposals UI (docs/AGENTS.md 5.11): no IO, no clock.
import type { ProposalDTO } from "../../api/gen/api.gen";

// --- Confirm flow ----------------------------------------------------------
// The hash sent on Confirm is the one the person was shown, held in the state.
// If the server's hash moves while the dialog is open, the flow goes to
// "changed" and nothing is sent until the person reviews again.

export type ConfirmState =
  | { tag: "idle" }
  | { tag: "reviewing"; id: string; hash: string }
  | { tag: "sending"; id: string; hash: string }
  | { tag: "changed"; id: string }
  | { tag: "failed"; id: string; message: string };

export type ConfirmInput =
  | { tag: "open"; id: string; hash: string }
  | { tag: "seen"; id: string; hash: string; pending: boolean }
  | { tag: "confirm" }
  | { tag: "cancel" }
  | { tag: "succeeded" }
  | { tag: "conflict" }
  | { tag: "failed"; message: string };

export type ConfirmCommand = { tag: "send"; id: string; hash: string } | { tag: "reload" };

export function stepConfirm(state: ConfirmState, input: ConfirmInput): [ConfirmState, ConfirmCommand[]] {
  switch (input.tag) {
    case "open":
      if (state.tag === "sending") return [state, []];
      return [{ tag: "reviewing", id: input.id, hash: input.hash }, []];
    case "seen":
      if (state.tag !== "reviewing" || state.id !== input.id) return [state, []];
      if (!input.pending) return [{ tag: "idle" }, []];
      if (input.hash !== state.hash) return [{ tag: "changed", id: state.id }, []];
      return [state, []];
    case "confirm":
      if (state.tag !== "reviewing") return [state, []];
      return [{ tag: "sending", id: state.id, hash: state.hash }, [{ tag: "send", id: state.id, hash: state.hash }]];
    case "cancel":
      return state.tag === "sending" ? [state, []] : [{ tag: "idle" }, []];
    case "succeeded":
      return state.tag === "sending" ? [{ tag: "idle" }, []] : [state, []];
    case "conflict":
      return state.tag === "sending" ? [{ tag: "changed", id: state.id }, [{ tag: "reload" }]] : [state, []];
    case "failed":
      return state.tag === "sending" ? [{ tag: "failed", id: state.id, message: input.message }, []] : [state, []];
  }
}

// --- Display ---------------------------------------------------------------

export const KIND_LABEL: Record<string, string> = {
  commit: "Commit",
  push: "Push",
  merge_request: "Open a merge request",
  install: "Install dependencies",
  publish: "Publish",
  agent_file: "Change agent files",
  delete_files: "Move files to _to_delete",
  subtasks: "Split into sub-tasks",
};

export const kindLabel = (kind: string): string => KIND_LABEL[kind] ?? "Change";

export type ProposalAction = "confirm" | "reject" | "retry";

export interface ProposalView {
  label: string;
  tone: "waiting" | "running" | "done" | "muted" | "problem";
  note?: string;
  actions: ProposalAction[];
}

/** Status text, explanation and the buttons that make sense for a proposal. */
export function describeProposal(p: Pick<ProposalDTO, "status" | "failKind" | "message" | "retryable">): ProposalView {
  switch (p.status) {
    case "pending":
      return { label: "Waiting for you", tone: "waiting", actions: ["confirm", "reject"] };
    case "confirmed":
      return { label: "Running", tone: "running", actions: [] };
    case "executed":
      return { label: "Done", tone: "done", actions: [] };
    case "rejected":
      return { label: "Rejected", tone: "muted", actions: [] };
    case "failed": {
      const actions: ProposalAction[] = p.retryable ? ["retry"] : [];
      if (p.failKind === "stale") {
        return {
          label: "Stale",
          tone: "problem",
          note: p.message || "The code changed after this was proposed. Ask the agent to propose it again.",
          actions,
        };
      }
      if (p.failKind === "interrupted") {
        return {
          label: "Interrupted",
          tone: "problem",
          note: p.message || "Yggdrasil stopped while this was running. Retry first checks what already happened.",
          actions,
        };
      }
      return { label: "Failed", tone: "problem", note: p.message || "This did not complete.", actions };
    }
    default:
      return { label: p.status, tone: "muted", actions: [] };
  }
}

export interface ProposalGroup {
  key: string;
  changeSetId?: string;
  items: ProposalDTO[];
}

/** Proposals of one change set sit together (RV-03); the rest are listed alone. Order is kept. */
export function groupByChangeSet(items: readonly ProposalDTO[]): ProposalGroup[] {
  const groups: ProposalGroup[] = [];
  const bySet = new Map<string, ProposalGroup>();
  for (const p of items) {
    if (!p.changeSetId) {
      groups.push({ key: p.id, items: [p] });
      continue;
    }
    let g = bySet.get(p.changeSetId);
    if (!g) {
      g = { key: p.changeSetId, changeSetId: p.changeSetId, items: [] };
      bySet.set(p.changeSetId, g);
      groups.push(g);
    }
    g.items.push(p);
  }
  return groups;
}
