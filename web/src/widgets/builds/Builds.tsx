import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { fetchBuilds } from "../../api/builds";
import { describeError } from "../../api/client";
import type { BuildDTO } from "../../api/gen/api.gen";
import { relativeTime } from "../../features/repos/ui";

const LIMIT = 8;
const label = (b: BuildDTO) => b.status.charAt(0).toUpperCase() + b.status.slice(1);
const tone = (s: string) =>
  s === "succeeded" ? "text-green-500" : s === "failed" ? "text-red-500" : s === "running" ? "text-accent" : "text-muted";
const active = (b: BuildDTO) => b.status === "queued" || b.status === "running";

/** Running and queued builds first, then the most recent finished ones. */
export function Builds() {
  const { data, error, isPending } = useQuery({
    queryKey: ["builds", "list", "", ""],
    queryFn: () => fetchBuilds({}),
    refetchInterval: 15_000,
  });

  if (isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(error)}
      </p>
    );
  }
  if (data.length === 0) return <p className="text-sm text-muted">No builds yet.</p>;
  // The list is newest first; a stable sort keeps that inside each group.
  const rows = [...data.filter(active), ...data.filter((b) => !active(b))].slice(0, LIMIT);
  return (
    <ul className="space-y-1 text-sm">
      {rows.map((b) => {
        const name = b.steps[0]?.name ?? "Build";
        return (
          <li key={b.id} className="flex flex-wrap items-baseline gap-2">
            <Link to="/builds/$buildId" params={{ buildId: b.id }} className="text-accent hover:underline">
              {name}
            </Link>
            <span className={tone(b.status)}>{label(b)}</span>
            <span className="ml-auto text-xs text-muted">{relativeTime(b.finishedAt ?? b.startedAt ?? b.queuedAt)}</span>
          </li>
        );
      })}
    </ul>
  );
}
