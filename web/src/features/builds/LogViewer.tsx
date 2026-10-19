import { useEffect, useRef, useState } from "react";
import { fetchStepLog } from "../../api/builds";
import { describeError } from "../../api/client";
import { TopicBuildPrefix } from "../../api/gen/api.gen";
import { useWS } from "../../api/wsContext";

/** Only this many lines stay rendered; the full log is on the server. */
const MAX_LINES = 5000;

function isLog(v: unknown): v is { step: number; attempt: number; line: string } {
  return typeof v === "object" && v !== null && typeof (v as { line?: unknown }).line === "string";
}

const cap = (lines: string[]) => (lines.length > MAX_LINES ? lines.slice(-MAX_LINES) : lines);

export function LogViewer({ buildId, step, attempt }: { buildId: string; step: number; attempt: number }) {
  const ws = useWS();
  const [lines, setLines] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [loaded, setLoaded] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  useEffect(() => {
    let live = true;
    let ready = false;
    const load = () => {
      ready = false;
      fetchStepLog(buildId, step, 0).then(
        (r) => {
          if (!live) return;
          setLines(cap(r.data === "" ? [] : r.data.replace(/\n$/, "").split("\n")));
          setError("");
          setLoaded(true);
          ready = true;
        },
        (e: unknown) => {
          if (live) setError(describeError(e));
        },
      );
    };
    const unsub = ws.subscribe(TopicBuildPrefix + buildId, {
      onEvent: (msg) => {
        // Lines that arrive before the first fetch are part of that fetch.
        if (!ready || !isLog(msg.data) || msg.data.step !== step || msg.data.attempt !== attempt) return;
        const line = msg.data.line;
        setLines((old) => cap([...old, line]));
      },
      onGap: load,
    });
    load();
    return () => {
      live = false;
      unsub();
    };
  }, [ws, buildId, step, attempt]);

  useEffect(() => {
    const el = box.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  return (
    <div>
      {error && <p role="alert" className="text-sm text-red-500">{error}</p>}
      <div
        ref={box}
        role="log"
        aria-label={`Log of step ${String(step + 1)}`}
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 24;
        }}
        className="h-80 overflow-auto rounded-md border border-line bg-surface p-2 font-mono text-xs whitespace-pre-wrap"
      >
        {loaded && lines.length === 0 && <span className="text-muted">No output yet.</span>}
        {lines.map((l, i) => (
          <div key={i}>{l}</div>
        ))}
      </div>
    </div>
  );
}
