import { Link } from "@tanstack/react-router";
import { describeError } from "../../api/client";
import { statusText, useBoard, useBoardLive } from "../../features/board";

const LIMIT = 6;

/** The last few tasks, newest first, each linking to its transcript. */
export function RecentChats() {
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
  const rows = data.cards
    .filter((c) => c.status !== "draft")
    .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
    .slice(0, LIMIT);
  if (rows.length === 0) return <p className="text-sm text-muted">No chats yet. Start a task to see it here.</p>;
  return (
    <ul className="space-y-1 text-sm">
      {rows.map((t) => (
        <li key={t.taskId} className="flex flex-wrap items-baseline justify-between gap-2">
          <Link to="/tasks/$taskId" params={{ taskId: t.taskId }} className="break-words text-accent hover:underline">
            {t.title}
          </Link>
          <span className="text-xs text-muted">{statusText(t)}</span>
        </li>
      ))}
    </ul>
  );
}
