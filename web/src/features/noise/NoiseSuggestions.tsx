import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { describeError } from "../../api/client";
import { newRequestId } from "../actions/hooks";
import { ProposalsByIds } from "../proposals";
import { evidenceText } from "./evidence";
import { createIgnoreProposal, fetchNoise } from "./api";

const noiseKey = (repoId: string) => ["noise", repoId] as const;

/**
 * Ignore suggestions for a repo with many untracked files (WS-09). Selecting entries creates a
 * pending proposal; the file is only changed after the person confirms it in the same review
 * every proposal uses.
 */
export function NoiseSuggestions({ repoId }: { repoId: string }) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: noiseKey(repoId), queryFn: () => fetchNoise(repoId) });
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set());
  const [created, setCreated] = useState<string | null>(null);
  // One key per selection: a retry of the same selection never creates a second proposal.
  const keyRef = useRef<{ sel: string; id: string } | null>(null);
  const m = useMutation({
    mutationFn: (entries: string[]) => {
      const sel = entries.join("\n");
      if (keyRef.current?.sel !== sel) keyRef.current = { sel, id: newRequestId() };
      return createIgnoreProposal(repoId, { requestId: keyRef.current.id, entries });
    },
    onSuccess: (p) => {
      setCreated(p.id);
      void qc.invalidateQueries({ queryKey: noiseKey(repoId) });
      void qc.invalidateQueries({ queryKey: ["proposals"] });
    },
  });

  if (q.isPending) return null;
  if (q.isError) {
    return (
      <p role="alert" className="text-sm text-danger">
        Ignore suggestions could not be read. {describeError(q.error)}
      </p>
    );
  }
  const d = q.data;
  if (!d.available) {
    return (
      <p data-noise="unavailable" className="text-sm text-warn">
        {d.message || "This repo could not be read, so there are no ignore suggestions."}
      </p>
    );
  }
  const pending = d.pendingProposalId ?? created;
  if (pending) {
    return (
      <section aria-label="Ignore suggestions" className="space-y-1">
        <p className="text-sm text-muted">An ignore proposal is waiting for you.</p>
        <ProposalsByIds ids={[pending]} />
      </section>
    );
  }
  if (d.candidates.length === 0) return null; // nothing to suggest: no clutter on the page

  const chosen = d.candidates.filter((c) => picked.has(c.pattern)).map((c) => c.pattern);
  const toggle = (pattern: string) => {
    setPicked((cur) => {
      const next = new Set(cur);
      if (next.has(pattern)) next.delete(pattern);
      else next.add(pattern);
      return next;
    });
  };
  return (
    <section aria-label="Ignore suggestions" className="space-y-2 rounded-md border border-line p-3">
      <h2 className="text-sm font-medium">Ignore suggestions</h2>
      <p className="text-xs text-muted">Many untracked files look generated. Nothing changes until you confirm.</p>
      <ul className="space-y-1">
        {d.candidates.map((c) => (
          <li key={c.pattern} className="text-sm">
            <label className="flex items-start gap-2">
              <input
                type="checkbox"
                checked={picked.has(c.pattern)}
                onChange={() => {
                  toggle(c.pattern);
                }}
              />
              <span>
                <span className="font-mono">{c.pattern}</span>
                <span className="block text-xs text-muted">{evidenceText(c)}</span>
              </span>
            </label>
          </li>
        ))}
      </ul>
      {m.isError ? (
        <p role="alert" className="text-sm text-danger">
          {describeError(m.error)}
        </p>
      ) : null}
      <button
        type="button"
        disabled={chosen.length === 0 || m.isPending}
        className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
        onClick={() => {
          m.mutate(chosen);
        }}
      >
        Create ignore proposal
      </button>
    </section>
  );
}
