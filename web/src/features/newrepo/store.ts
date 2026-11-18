import { create } from "zustand";

// UI-only memory (docs/AGENTS.md 5.10): the spec hash and wording the person confirmed in this
// browser session, keyed by operation ID, so an explicit Retry can re-confirm exactly that hash.
// It is not a copy of server state and is not kept across a reload.

export interface RememberedSpec {
  kind: "init" | "publish";
  hash: string;
  lines: string[];
}

interface SpecMemory {
  specs: Record<string, Partial<Record<RememberedSpec["kind"], RememberedSpec>>>;
  remember: (operationId: string, spec: RememberedSpec) => void;
}

export const useSpecMemory = create<SpecMemory>()((set) => ({
  specs: {},
  remember: (operationId, spec) => {
    set((s) => ({ specs: { ...s.specs, [operationId]: { ...s.specs[operationId], [spec.kind]: spec } } }));
  },
}));
