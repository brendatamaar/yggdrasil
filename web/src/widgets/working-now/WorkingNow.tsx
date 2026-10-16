import { Link } from "@tanstack/react-router";
import { describeError } from "../../api/client";
import { useRunningTasks } from "../../api/queries";

/** Tasks that are running right now, each linking to its transcript. */
export function WorkingNow() {
  const { data, error, isPending } = useRunningTasks();

  if (isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(error)}
      </p>
    );
  }
  if (data.length === 0) return <p className="text-sm text-muted">Nothing is running.</p>;
  return (
    <ul className="space-y-1 text-sm">
      {data.map((t) => (
        <li key={t.id}>
          <Link to="/tasks/$taskId" params={{ taskId: t.id }} className="text-accent hover:underline">
            {t.title}
          </Link>
        </li>
      ))}
    </ul>
  );
}
