import { useEffect, useLayoutEffect, useReducer, useRef } from "react";
import { describeError } from "../../api/client";
import { followStep, lineKey, type Follow } from "./core";
import { useServerLog } from "./hooks";

const NEAR_BOTTOM_PX = 24;

/** The live log of one server: the tail, then streamed lines. Scrolling up pauses the auto-scroll. */
export function LogView({ serverId, title }: { serverId: string; title: string }) {
  const q = useServerLog(serverId);
  const box = useRef<HTMLDivElement>(null);
  const [follow, dispatch] = useReducer(followStep, { tag: "following" } as Follow);
  const count = q.data?.lines.length ?? 0;

  useLayoutEffect(() => {
    const el = box.current;
    if (el && follow.tag === "following") el.scrollTop = el.scrollHeight;
  }, [count, follow]);

  // A Resume click re-pins to the end even if no line arrives.
  useEffect(() => {
    const el = box.current;
    if (el && follow.tag === "following") el.scrollTop = el.scrollHeight;
  }, [follow.tag]);

  if (q.isPending) return <p className="text-sm text-muted">Loading the log…</p>;
  if (q.isError) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(q.error)}
      </p>
    );
  }

  return (
    <div className="space-y-1">
      {q.data.incomplete ? (
        <p className="text-sm text-warn">Some output was lost, so this log is incomplete.</p>
      ) : null}
      <div
        ref={box}
        role="log"
        aria-label={`Log of ${title}`}
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget;
          dispatch({ tag: "scrolled", atBottom: el.scrollHeight - el.scrollTop - el.clientHeight <= NEAR_BOTTOM_PX });
        }}
        className="max-h-64 overflow-auto rounded border border-line bg-canvas p-2 font-mono text-xs"
      >
        {q.data.lines.length === 0 ? <p className="text-muted">No output yet.</p> : null}
        {q.data.lines.map((l) => (
          <div key={lineKey(l)} className={l.stream === "stderr" ? "whitespace-pre-wrap text-warn" : "whitespace-pre-wrap"}>
            {l.line}
          </div>
        ))}
      </div>
      {follow.tag === "paused" ? (
        <button
          type="button"
          className="rounded border border-line px-2 py-0.5 text-xs hover:bg-surface"
          onClick={() => {
            dispatch({ tag: "resume" });
          }}
        >
          Resume auto-scroll
        </button>
      ) : null}
    </div>
  );
}
