import type { RepoStatusDTO } from "../../api/gen/api.gen";
import { btn } from "../repos/ui";
import { emptyRow, type Row } from "./rows";
import { input } from "./ui";

/** One step per row: repo, name and the command split into arguments. No shell syntax. */
export function StepRows({
  rows,
  repos,
  onChange,
}: {
  rows: Row[];
  repos: RepoStatusDTO[];
  onChange: (rows: Row[]) => void;
}) {
  const set = (i: number, patch: Partial<Row>) => {
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  };
  return (
    <div className="flex flex-col gap-2">
      <p className="text-xs text-muted">
        One command per step. Arguments are split on spaces (use quotes to keep a space). Shell syntax such as
        &amp;&amp; or | is not supported: add another step instead.
      </p>
      {rows.map((r, i) => (
        <div key={i} className="flex flex-wrap items-center gap-2">
          <select
            aria-label={`Step ${String(i + 1)} repo`}
            className={input}
            value={r.repoId}
            onChange={(e) => {
              set(i, { repoId: e.target.value });
            }}
          >
            <option value="">Repo...</option>
            {repos.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
          <input
            aria-label={`Step ${String(i + 1)} name`}
            placeholder="Name"
            className={`${input} w-28`}
            value={r.name}
            onChange={(e) => {
              set(i, { name: e.target.value });
            }}
          />
          <input
            aria-label={`Step ${String(i + 1)} command`}
            placeholder="pnpm test"
            className={`${input} min-w-48 flex-1 font-mono`}
            value={r.command}
            onChange={(e) => {
              set(i, { command: e.target.value });
            }}
          />
          <button
            type="button"
            className={btn}
            aria-label={`Remove step ${String(i + 1)}`}
            onClick={() => {
              onChange(rows.filter((_, j) => j !== i));
            }}
          >
            Remove
          </button>
        </div>
      ))}
      <div>
        <button
          type="button"
          className={btn}
          onClick={() => {
            onChange([...rows, emptyRow(rows.at(-1)?.repoId)]);
          }}
        >
          Add step
        </button>
      </div>
    </div>
  );
}
