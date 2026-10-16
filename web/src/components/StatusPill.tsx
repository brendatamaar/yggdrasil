import { useRunningTasks } from "../api/queries";
import type { ConnectionStatus } from "../api/ws";
import { useConnectionStore } from "../stores/connection";

const labels: Record<ConnectionStatus, string> = {
  connecting: "Connecting",
  open: "Connected",
  reconnecting: "Reconnecting",
  closed: "Offline",
};

const dots: Record<ConnectionStatus, string> = {
  connecting: "bg-warn",
  open: "bg-ok",
  reconnecting: "bg-warn",
  closed: "bg-danger",
};

export function StatusPill() {
  const status = useConnectionStore((s) => s.status);
  // Zero while loading or when the server can't be reached: the connection
  // dot already says so.
  const running = useRunningTasks().data?.length ?? 0;

  return (
    <div
      role="status"
      className="flex items-center gap-2 rounded-full border border-line px-3 py-1 text-sm"
    >
      <span aria-hidden className={`size-2 rounded-full ${dots[status]}`} />
      <span>{labels[status]}</span>
      <span className="text-muted">{running} running</span>
    </div>
  );
}
