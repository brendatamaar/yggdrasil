import type { ReactNode } from "react";
import {
  EventCommand,
  EventError,
  EventFileEdit,
  EventMessage,
  EventStatus,
  EventSubagentEnd,
  EventSubagentStart,
  EventToolCall,
  EventToolResult,
  EventUsage,
  RoleUser,
  RunCancelled,
  RunCompleted,
  RunFailed,
  RunStarted,
  type Event as AgentEvent,
} from "../../api/gen/agents.gen";
import { formatCost, formatDuration, formatTokens } from "./format";
import { agentStats, buildTree, type TreeNode } from "./tree";

// Everything the server sends is shown as text. Nothing here sets HTML, so
// output from an agent can't inject markup.

const statusText: Record<string, string> = {
  [RunStarted]: "The agent started.",
  [RunCompleted]: "The agent finished.",
  [RunFailed]: "The agent stopped with an error.",
  [RunCancelled]: "The run was cancelled.",
};

function inputText(input: unknown): string {
  if (input === undefined) return "";
  return JSON.stringify(input, null, 2);
}

function Block({ children, tone }: { children: ReactNode; tone?: "error" }) {
  return (
    <div
      className={`rounded border px-3 py-2 text-sm ${tone === "error" ? "border-danger text-danger" : "border-line"}`}
    >
      {children}
    </div>
  );
}

function EventView({ event }: { event: AgentEvent }) {
  switch (event.kind) {
    case EventMessage:
      return (
        <div className="text-sm">
          <span className="font-medium text-muted">{event.role === RoleUser ? "Prompt" : "Agent"}</span>
          <p className="mt-0.5 whitespace-pre-wrap">{event.text}</p>
        </div>
      );
    case EventToolCall:
      return (
        <details className="rounded border border-line px-3 py-1 text-sm">
          <summary className="cursor-pointer">Tool: {event.tool?.name ?? "unknown"}</summary>
          <pre className="mt-1 overflow-x-auto text-xs">{inputText(event.tool?.input)}</pre>
        </details>
      );
    case EventToolResult:
      return (
        <details className={`rounded border px-3 py-1 text-sm ${event.isError ? "border-danger" : "border-line"}`}>
          <summary className="cursor-pointer">{event.isError ? "Tool result (error)" : "Tool result"}</summary>
          <pre className="mt-1 overflow-x-auto whitespace-pre-wrap text-xs">{event.text}</pre>
        </details>
      );
    case EventFileEdit:
      return (
        <details className="rounded border border-line px-3 py-1 text-sm" open={Boolean(event.file?.diff)}>
          <summary className="cursor-pointer">
            File {event.file?.op ?? "changed"}: <code>{event.file?.path}</code>
          </summary>
          {event.file?.diff ? <pre className="mt-1 overflow-x-auto text-xs">{event.file.diff}</pre> : null}
        </details>
      );
    case EventCommand:
      return (
        <div className="rounded border border-line px-3 py-1 text-sm">
          <code>$ {event.command?.line}</code>
        </div>
      );
    case EventError:
      return (
        <div role="alert">
          <Block tone="error">{event.error}</Block>
        </div>
      );
    case EventStatus:
      return <p className="text-xs text-muted">{statusText[event.status ?? ""] ?? event.status}</p>;
    default:
      return null;
  }
}

function SubAgent({ node }: { node: TreeNode }) {
  const stats = agentStats(node);
  const tokens = stats.inputTokens + stats.outputTokens;
  return (
    <details open className="rounded border border-accent/50 px-3 py-2">
      <summary className="cursor-pointer text-sm font-medium">
        Sub-agent{node.event.text ? `: ${node.event.text}` : ""}
        {node.orphan ? " (detached)" : ""}
      </summary>
      <p className="mt-1 text-xs text-muted">
        {stats.toolCalls} tool call{stats.toolCalls === 1 ? "" : "s"}
        {stats.durationMs === undefined ? "" : ` · ${formatDuration(stats.durationMs)}`}
        {tokens > 0 ? ` · ${formatTokens(tokens)} tokens` : ""}
      </p>
      <NodeList nodes={node.children} />
    </details>
  );
}

function NodeList({ nodes }: { nodes: TreeNode[] }) {
  return (
    <ol className="mt-2 space-y-2">
      {nodes.map((n) => {
        // Usage is summed in the header and a sub-agent's end is shown by its stats.
        if (n.event.kind === EventUsage || n.event.kind === EventSubagentEnd) return null;
        return (
          <li key={n.event.seq}>
            {n.event.kind === EventSubagentStart ? <SubAgent node={n} /> : <EventView event={n.event} />}
          </li>
        );
      })}
    </ol>
  );
}

/** The transcript of a task: messages, tool calls, edits and a tree of sub-agents. */
export function Transcript({ events }: { events: readonly AgentEvent[] }) {
  const roots = buildTree(events);
  if (roots.length === 0) return <p className="text-sm text-muted">Nothing has happened yet.</p>;
  return (
    <section aria-label="Transcript">
      <NodeList nodes={roots} />
    </section>
  );
}

export function UsageSummary({ inputTokens, outputTokens, costUsd, turns }: {
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  turns: number;
}) {
  const tokens = inputTokens + outputTokens;
  if (tokens === 0 && costUsd === 0 && turns === 0) return null;
  return (
    <p className="text-sm text-muted">
      {formatTokens(tokens)} tokens
      {costUsd > 0 ? ` · ${formatCost(costUsd)}` : ""}
      {turns > 0 ? ` · ${String(turns)} turn${turns === 1 ? "" : "s"}` : ""}
    </p>
  );
}
