import { serverId } from "../../api/devservers";
import { describeError } from "../../api/client";
import type { DevServerDTO } from "../../api/gen/api.gen";
import { portHref, serverButtons, statusLabel, statusTone } from "./core";
import { useServerAction } from "./hooks";

const btn = "rounded border border-line px-2 py-0.5 text-sm hover:bg-surface disabled:opacity-50";

/** Detected ports as links to the local server. A port outside the worktree's block gets a warning. */
export function PortList({ server }: { server: DevServerDTO }) {
  if (server.ports.length === 0) return null;
  return (
    <ul aria-label="Ports" className="flex flex-wrap gap-x-3 gap-y-1 text-sm">
      {server.ports.map((p) => (
        <li key={p.port} className="flex items-center gap-1">
          <a href={portHref(p.port)} target="_blank" rel="noreferrer" className="font-mono text-accent hover:underline">
            {`127.0.0.1:${String(p.port)}`}
          </a>
          {p.outsideBlock ? (
            <span className="text-xs text-warn" title="This port is outside the block reserved for this worktree. It may collide with another worktree.">
              Outside this worktree's port block
            </span>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

/** Start, Stop and Restart for one server, showing only what its state allows. */
export function ServerControls({ server, label }: { server: DevServerDTO; label?: string }) {
  const act = useServerAction();
  const id = serverId(server);
  const b = serverButtons(server.status);
  const name = label ?? server.command;
  const run = (action: "start" | "stop" | "restart") => {
    act.mutate({ id, action });
  };
  return (
    <div className="flex flex-wrap items-center gap-2">
      {b.start ? (
        <button type="button" className={btn} disabled={act.isPending} aria-label={`Start ${name}`} onClick={() => { run("start"); }}>
          Start
        </button>
      ) : null}
      {b.stop ? (
        <button type="button" className={btn} disabled={act.isPending} aria-label={`Stop ${name}`} onClick={() => { run("stop"); }}>
          Stop
        </button>
      ) : null}
      {b.restart ? (
        <button type="button" className={btn} disabled={act.isPending} aria-label={`Restart ${name}`} onClick={() => { run("restart"); }}>
          Restart
        </button>
      ) : null}
      {act.error ? (
        <span role="alert" className="text-sm text-danger">
          {describeError(act.error)}
        </span>
      ) : null}
    </div>
  );
}

export function StatusText({ server }: { server: DevServerDTO }) {
  return <span className={`text-sm ${statusTone(server.status)}`}>{statusLabel(server.status)}</span>;
}

/** The plain-language reason a server failed, or what an orphan is. */
export function ServerMessage({ server }: { server: DevServerDTO }) {
  if (server.status === "orphaned") {
    return (
      <p className="text-sm text-danger">
        This server was left running by an earlier session of Yggdrasil. Stop it to free its ports.
      </p>
    );
  }
  return server.message ? <p className="text-sm text-danger">{server.message}</p> : null;
}
