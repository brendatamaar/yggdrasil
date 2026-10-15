import { useConnectionStore } from "../stores/connection";
import type { ConnectionStatus } from "../api/ws";

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

/** Running task count. No task list endpoint exists in api.gen yet, so this is 0 until one lands. */
function useRunningCount(): number {
  return 0;
}

export function StatusPill() {
  const status = useConnectionStore((s) => s.status);
  const running = useRunningCount();

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
