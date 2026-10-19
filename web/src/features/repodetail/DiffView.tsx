import { useMemo } from "react";
import type { DiffResponse } from "../../api/gen/api.gen";
import { parseDiff, type DiffLineKind } from "./parseDiff";

const rowClass: Record<DiffLineKind, string> = {
  add: "bg-ok/15",
  del: "bg-danger/15",
  ctx: "",
  hunk: "bg-surface text-accent",
  meta: "text-muted",
};
const mark: Record<DiffLineKind, string> = { add: "+", del: "-", ctx: " ", hunk: "", meta: "" };

/** A unified diff with line numbers. Scrolls sideways inside its own pane. */
export function DiffView({ diff, label }: { diff: DiffResponse; label: string }) {
  const lines = useMemo(() => parseDiff(diff.text), [diff.text]);
  return (
    <div>
      {diff.binary && <p className="mb-1 text-sm text-warn">Binary files can't be shown as text.</p>}
      {diff.truncated && <p className="mb-1 text-sm text-warn">This diff is too large; only the start is shown.</p>}
      {lines.length === 0 ? (
        !diff.binary && <p className="text-sm text-muted">No changes to show.</p>
      ) : (
        <div role="region" aria-label={label} tabIndex={0} className="overflow-x-auto rounded-md border border-line font-mono text-xs">
          <table className="w-full border-collapse">
            <tbody>
              {lines.map((l, i) => (
                <tr key={i} data-kind={l.kind} className={rowClass[l.kind]}>
                  <td aria-hidden className="select-none px-2 text-right text-muted">{l.oldNo ?? ""}</td>
                  <td aria-hidden className="select-none px-2 text-right text-muted">{l.newNo ?? ""}</td>
                  <td className="whitespace-pre pr-3">
                    <span aria-hidden className="select-none">{mark[l.kind]}</span>
                    {l.text}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
