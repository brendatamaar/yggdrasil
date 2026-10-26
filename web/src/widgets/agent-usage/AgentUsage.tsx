import { describeError } from "../../api/client";
import { costText, useBoard, useBoardLive } from "../../features/board";
import { formatTokens } from "../../features/transcript/format";
import { usageRows } from "./usage";

const LIMIT = 8;

/** Tokens and cost per day and agent, as a table with a simple bar for the tokens. */
export function AgentUsage() {
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
  const rows = usageRows(data.cards)
    .filter((r) => r.tokens > 0)
    .slice(0, LIMIT);
  if (rows.length === 0) return <p className="text-sm text-muted">No usage yet. Tokens and cost show up once an agent has run.</p>;
  const max = Math.max(...rows.map((r) => r.tokens));
  return (
    <table className="w-full text-left text-sm">
      <caption className="sr-only">Tokens and cost per day and agent</caption>
      <thead className="text-xs text-muted">
        <tr>
          <th scope="col" className="py-1 pr-2 font-normal">
            Day
          </th>
          <th scope="col" className="pr-2 font-normal">
            Agent
          </th>
          <th scope="col" className="pr-2 font-normal">
            Tokens
          </th>
          <th scope="col" className="font-normal">
            Cost
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={`${r.day}/${r.agent}`}>
            <td className="py-0.5 pr-2">{r.day}</td>
            <td className="pr-2">{r.agent}</td>
            <td className="pr-2">
              <div className="flex items-center gap-2">
                <span aria-hidden="true" className="h-2 rounded-sm bg-accent" style={{ width: `${String(Math.max(4, Math.round((r.tokens / max) * 60)))}px` }} />
                <span>{formatTokens(r.tokens)}</span>
              </div>
            </td>
            <td>{r.costKnown ? costText(r) : "not reported"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
