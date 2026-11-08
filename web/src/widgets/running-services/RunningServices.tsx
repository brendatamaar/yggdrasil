import { describeError } from "../../api/client";
import { serverId } from "../../api/devservers";
import { PortList, ServerControls, ServerMessage, sortServers, StatusText, useDevServers } from "../../features/devservers";

/** Every dev server across repos and worktrees, with state, ports and controls (UI-03, DV-01 to DV-03). */
export function RunningServices() {
  const q = useDevServers();
  if (q.isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (q.isError) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(q.error)}
      </p>
    );
  }
  if (q.data.length === 0) return <p className="text-sm text-muted">No dev servers yet.</p>;
  return (
    <ul className="space-y-2 text-sm">
      {sortServers(q.data).map((s) => (
        <li key={serverId(s)} className="space-y-0.5 border-b border-line pb-2 last:border-b-0">
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span className="font-medium">{s.repoName}</span>
            <span className="text-xs text-muted">{s.worktreeId ? "Task worktree" : "Main checkout"}</span>
            <span className="font-mono text-xs">{s.command}</span>
            <StatusText server={s} />
          </div>
          <ServerMessage server={s} />
          <PortList server={s} />
          <ServerControls server={s} label={`${s.repoName} ${s.command}`} />
        </li>
      ))}
    </ul>
  );
}
