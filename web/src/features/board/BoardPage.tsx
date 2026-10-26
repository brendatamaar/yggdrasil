import { useId, type KeyboardEvent } from "react";
import { describeError } from "../../api/client";
import { useBoard, useBoardLive, useCancelTask } from "./hooks";
import { TaskCard } from "./TaskCard";
import { columns, groupCards, useBoardUI } from "./ui";

/** Moves focus between card links with the arrow keys: up/down in a column, left/right across columns. */
function onBoardKey(e: KeyboardEvent<HTMLElement>) {
  const target = e.target as HTMLElement;
  if (!target.hasAttribute("data-card-link")) return;
  const key = e.key;
  if (!["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"].includes(key)) return;
  const cols = Array.from(e.currentTarget.querySelectorAll<HTMLElement>("[data-column]"));
  const col = target.closest<HTMLElement>("[data-column]");
  if (!col) return;
  const links = (c: HTMLElement) => Array.from(c.querySelectorAll<HTMLElement>("[data-card-link]"));
  const mine = links(col);
  const at = mine.indexOf(target);
  let next: HTMLElement | undefined;
  if (key === "ArrowDown") next = mine[at + 1];
  else if (key === "ArrowUp") next = mine[at - 1];
  else {
    const step = key === "ArrowRight" ? 1 : -1;
    for (let i = cols.indexOf(col) + step; i >= 0 && i < cols.length && !next; i += step) {
      const l = links(cols[i] as HTMLElement);
      next = l[Math.min(at, l.length - 1)];
    }
  }
  if (next) {
    e.preventDefault();
    next.focus();
  }
}

export function BoardPage() {
  useBoardLive();
  const { data, error, isPending, refetch } = useBoard();
  const cancel = useCancelTask();
  const doneCollapsed = useBoardUI((s) => s.doneCollapsed);
  const toggleDone = useBoardUI((s) => s.toggleDone);
  const headingId = useId();

  return (
    <section aria-labelledby={headingId} className="space-y-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h1 id={headingId} className="text-lg font-semibold">
          Agent board
        </h1>
        {data && (
          <p className="text-sm text-muted">
            {data.active} of {data.maxAgents} agents active
            {data.hint.raiseTo > 0 && ` · tasks are waiting; you could allow ${String(data.hint.raiseTo)} agents`}
          </p>
        )}
      </div>

      {isPending && <p className="text-sm text-muted">Loading the board…</p>}
      {error && (
        <div role="alert" className="text-sm text-danger">
          <p>{describeError(error)}</p>
          <button
            type="button"
            onClick={() => {
              void refetch();
            }}
            className="mt-1 rounded-md border border-line px-2 py-0.5 text-fg hover:bg-surface"
          >
            Try again
          </button>
        </div>
      )}
      {cancel.error && (
        <p role="alert" className="text-sm text-danger">
          {describeError(cancel.error)}
        </p>
      )}

      {data && (
        <div onKeyDown={onBoardKey} className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
          {columns.map((col) => {
            const cards = groupCards(data.cards)[col.id];
            const collapsed = col.id === "done" && doneCollapsed;
            return (
              <section key={col.id} aria-label={col.title} data-column={col.id} className="min-w-0 rounded-md border border-line p-2">
                <h2 className="mb-2 flex items-center justify-between text-sm font-semibold">
                  <span>
                    {col.title} <span className="font-normal text-muted">({cards.length})</span>
                  </span>
                  {col.id === "done" && (
                    <button
                      type="button"
                      aria-expanded={!collapsed}
                      onClick={toggleDone}
                      className="rounded-sm px-1 text-xs font-normal text-muted hover:text-fg"
                    >
                      {collapsed ? "Show" : "Hide"}
                    </button>
                  )}
                </h2>
                {!collapsed &&
                  (cards.length === 0 ? (
                    <p className="text-sm text-muted">{col.empty}</p>
                  ) : (
                    <ul aria-label={`${col.title} tasks`} className="space-y-2">
                      {cards.map((c) => (
                        <TaskCard
                          key={c.taskId}
                          card={c}
                          cancelling={cancel.isPending && cancel.variables === c.taskId}
                          onCancel={(id) => {
                            cancel.mutate(id);
                          }}
                        />
                      ))}
                    </ul>
                  ))}
              </section>
            );
          })}
        </div>
      )}
    </section>
  );
}
