import { describeError } from "../../api/client";
import { useHealth } from "../../api/queries";
import { useConnectionStore } from "../../stores/connection";

const connection = {
  connecting: "Connecting",
  open: "Connected",
  reconnecting: "Reconnecting",
  closed: "Offline",
} as const;

/** The local Yggdrasil server: its version and whether the page is connected to it. */
export function ThisMachine() {
  const { data, error } = useHealth();
  const status = useConnectionStore((s) => s.status);

  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
      <dt className="text-muted">Version</dt>
      <dd>{data ? data.version : "—"}</dd>
      <dt className="text-muted">Connection</dt>
      <dd>{connection[status]}</dd>
      {error ? (
        <dd role="alert" className="col-span-2 text-danger">
          {describeError(error)}
        </dd>
      ) : null}
    </dl>
  );
}
