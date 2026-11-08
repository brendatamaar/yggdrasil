import { useState } from "react";
import { describeError } from "../../api/client";
import { serverId } from "../../api/devservers";
import type { DevServerDTO } from "../../api/gen/api.gen";
import { sortServers } from "./core";
import { useDevServers } from "./hooks";
import { LogView } from "./LogView";
import { PortList, ServerControls, ServerMessage, StatusText } from "./ServerRow";

export interface PanelScope {
  repoId: string;
  /** Worktree IDs to show (a task page). Without it the panel shows the repo's main checkout. */
  worktreeIds?: readonly string[];
}

const inScope = (s: DevServerDTO, scope: PanelScope): boolean => {
  if (s.repoId !== scope.repoId) return false;
  if (scope.worktreeIds) return s.worktreeId !== undefined && scope.worktreeIds.includes(s.worktreeId);
  return s.worktreeId === undefined;
};

/** Dev servers of a repo or of a task's worktrees: state, ports, controls and a live log (DV-01 to DV-03). */
export function DevServerPanel({ scope, heading = "Dev servers" }: { scope: PanelScope; heading?: string }) {
  const q = useDevServers();
  const [logFor, setLogFor] = useState<string | null>(null);

  return (
    <section aria-label={heading} className="space-y-2 rounded border border-line p-3">
      <h2 className="font-medium">{heading}</h2>
      {q.isPending ? <p className="text-sm text-muted">Loading…</p> : null}
      {q.isError ? (
        <p role="alert" className="text-sm text-danger">
          {describeError(q.error)}
        </p>
      ) : null}
      {q.data ? <Rows servers={sortServers(q.data.filter((s) => inScope(s, scope)))} logFor={logFor} setLogFor={setLogFor} /> : null}
    </section>
  );
}

function Rows({ servers, logFor, setLogFor }: { servers: DevServerDTO[]; logFor: string | null; setLogFor: (id: string | null) => void }) {
  if (servers.length === 0) return <p className="text-sm text-muted">No dev servers found here yet.</p>;
  return (
    <ul className="space-y-3">
      {servers.map((s) => {
        const id = serverId(s);
        const open = logFor === id;
        return (
          <li key={id} className="space-y-1">
            <div className="flex flex-wrap items-center gap-3">
              <span className="font-mono text-sm">{s.command}</span>
              <StatusText server={s} />
              <ServerControls server={s} />
              <button
                type="button"
                aria-expanded={open}
                className="ml-auto rounded border border-line px-2 py-0.5 text-sm hover:bg-surface"
                onClick={() => {
                  setLogFor(open ? null : id);
                }}
              >
                {open ? "Hide log" : "Show log"}
                <span className="sr-only">{` for ${s.command}`}</span>
              </button>
            </div>
            <ServerMessage server={s} />
            <PortList server={s} />
            {open ? <LogView serverId={id} title={s.command} /> : null}
          </li>
        );
      })}
    </ul>
  );
}
