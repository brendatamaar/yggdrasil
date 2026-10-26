import { Link } from "@tanstack/react-router";
import { describeError } from "../../api/client";
import { elapsedText, useBoard, useBoardLive } from "../../features/board";

/** Tasks that are working right now (from the board), each linking to its transcript. */
export function WorkingNow() {
  useBoardLive();
  const { data, error, isPending } = useBoard();

  if (isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(error)}
      </p>
    );
  }
  const running = data.cards.filter((c) => c.status === "running");
  if (running.length === 0) return <p className="text-sm text-muted">Nothing is running.</p>;
  return (
    <ul className="space-y-1 text-sm">
      {running.map((t) => (
        <li key={t.taskId} className="flex flex-wrap items-baseline justify-between gap-2">
          <Link to="/tasks/$taskId" params={{ taskId: t.taskId }} className="break-words text-accent hover:underline">
            {t.title}
          </Link>
          <span className="text-xs text-muted">{elapsedText(t)}</span>
        </li>
      ))}
    </ul>
  );
}
