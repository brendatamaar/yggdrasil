import * as Tooltip from "@radix-ui/react-tooltip";
import { useId, useMemo, useState, type ReactNode } from "react";
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
  SubagentsNested,
  type Capabilities,
  type Event as AgentEvent,
} from "../../api/gen/agents.gen";
import { formatCost, formatDuration, formatTokens } from "./format";
import { capText, diffLines, inputText, summarizeInput } from "./summary";
import { agentStats, buildTree, groupTurns, type TreeNode, type UsageTotals } from "./tree";

// Everything the server sends is shown as text. Nothing here sets HTML or
// parses markdown, so output from an agent can't inject markup (AGENTS.md 6).

/** Roots shown before "Show earlier events"; keeps long runs responsive. */
const VISIBLE_ROOTS = 200;

const statusText: Record<string, string> = {
  [RunStarted]: "The agent started.",
  [RunCompleted]: "The agent finished.",
  [RunFailed]: "The agent stopped with an error.",
  [RunCancelled]: "The run was cancelled.",
};

// ---- small parts --------------------------------------------------------

/** A button that opens and closes a region; keyboard operable, announces its state. */
function Disclosure({
  label,
  summary,
  defaultOpen = false,
  tone,
  children,
}: {
  label: ReactNode;
  summary?: ReactNode;
  defaultOpen?: boolean;
  tone?: "error";
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const id = useId();
  return (
    <div className={`rounded border text-sm ${tone === "error" ? "border-danger" : "border-line"}`}>
      <button
        type="button"
        className="flex w-full items-baseline gap-2 px-3 py-1 text-left"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => {
          setOpen((o) => !o);
        }}
      >
        <span aria-hidden="true" className="text-muted">
          {open ? "▾" : "▸"}
        </span>
        <span className="font-medium">{label}</span>
        {summary ? <span className="min-w-0 flex-1 truncate text-muted">{summary}</span> : null}
      </button>
      <div id={id} hidden={!open} className="px-3 pb-2">
        {open ? children : null}
      </div>
    </div>
  );
}

/** Plain text that is cut to a few lines until asked for. */
function CappedText({ text, className }: { text: string; className?: string }) {
  const [all, setAll] = useState(false);
  const { shown, hidden } = useMemo(() => capText(text), [text]);
  return (
    <>
      <pre className={`overflow-x-auto whitespace-pre-wrap break-words text-xs ${className ?? ""}`}>{all ? text : shown}</pre>
      {hidden > 0 && !all ? (
        <button
          type="button"
          className="text-xs text-accent underline"
          onClick={() => {
            setAll(true);
          }}
        >
          Show more ({formatTokens(hidden)} characters hidden)
        </button>
      ) : null}
    </>
  );
}

const diffTone: Record<string, string> = {
  add: "text-ok",
  del: "text-danger",
  hunk: "text-accent",
  meta: "text-muted",
  ctx: "",
};

function DiffView({ diff }: { diff: string }) {
  const [all, setAll] = useState(false);
  const lines = useMemo(() => diffLines(diff), [diff]);
  const max = 200;
  const shown = all ? lines : lines.slice(0, max);
  return (
    <>
      <pre className="overflow-x-auto text-xs" aria-label="Changes">
        {shown.map((l, i) => (
          <span key={i} className={`block whitespace-pre ${diffTone[l.kind] ?? ""}`}>
            {l.text || " "}
          </span>
        ))}
      </pre>
      {lines.length > max && !all ? (
        <button
          type="button"
          className="text-xs text-accent underline"
          onClick={() => {
            setAll(true);
          }}
        >
          Show more ({String(lines.length - max)} lines hidden)
        </button>
      ) : null}
    </>
  );
}

// ---- events ---------------------------------------------------------------

interface RenderCtx {
  /** Tool results by the ID of the call they answer. */
  results: Map<string, AgentEvent>;
  /** IDs of tool calls in the transcript; a result for one of them shows inside the call. */
  callIds: Set<string>;
  capabilities?: Capabilities;
}

function ToolCallView({ event, result }: { event: AgentEvent; result?: AgentEvent }) {
  const name = event.tool?.name ?? "unknown";
  const full = inputText(event.tool?.input);
  const tone = result?.isError ? "error" : undefined;
  const state = result ? (result.isError ? "failed" : "done") : "";
  return (
    <Disclosure
      tone={tone}
      label={`Tool: ${name}`}
      summary={
        <>
          {summarizeInput(event.tool?.input)}
          {state ? ` · ${state}` : ""}
        </>
      }
    >
      {full ? (
        <>
          <p className="text-xs font-medium text-muted">Input</p>
          <CappedText text={full} />
        </>
      ) : null}
      {result ? (
        <>
          <p className="mt-1 text-xs font-medium text-muted">{result.isError ? "Error" : "Result"}</p>
          <CappedText text={result.text ?? ""} className={result.isError ? "text-danger" : ""} />
        </>
      ) : null}
    </Disclosure>
  );
}

function CommandView({ event }: { event: AgentEvent }) {
  const c = event.command;
  const exit = c?.exitCode;
  const failed = exit !== undefined && exit !== 0;
  return (
    <Disclosure
      defaultOpen={failed}
      tone={failed ? "error" : undefined}
      label={<code>$ {c?.line}</code>}
      summary={exit === undefined ? "running" : `exit code ${String(exit)}`}
    >
      {c?.output ? <CappedText text={c.output} /> : <p className="text-xs text-muted">No output.</p>}
    </Disclosure>
  );
}

function FileEditView({ event }: { event: AgentEvent }) {
  const f = event.file;
  return (
    <Disclosure defaultOpen={Boolean(f?.diff)} label={`File ${f?.op ?? "changed"}`} summary={<code>{f?.path}</code>}>
      {f?.diff ? <DiffView diff={f.diff} /> : <p className="text-xs text-muted">No diff available.</p>}
    </Disclosure>
  );
}

function EventView({ event, ctx }: { event: AgentEvent; ctx: RenderCtx }) {
  switch (event.kind) {
    case EventMessage:
      return (
        <div className="text-sm">
          <span className="font-medium text-muted">{event.role === RoleUser ? "Prompt" : "Agent"}</span>
          <CappedText text={event.text ?? ""} className="mt-0.5 !text-sm" />
        </div>
      );
    case EventToolCall:
      return <ToolCallView event={event} result={event.id ? ctx.results.get(event.id) : undefined} />;
    case EventToolResult:
      return (
        <Disclosure tone={event.isError ? "error" : undefined} label={event.isError ? "Tool result (error)" : "Tool result"}>
          <CappedText text={event.text ?? ""} />
        </Disclosure>
      );
    case EventFileEdit:
      return <FileEditView event={event} />;
    case EventCommand:
      return <CommandView event={event} />;
    case EventError:
      return (
        <div role="alert" className="rounded border border-danger px-3 py-2 text-sm text-danger">
          {event.error}
        </div>
      );
    case EventStatus:
      return <p className="text-xs text-muted">{statusText[event.status ?? ""] ?? event.status}</p>;
    default:
      return null;
  }
}

function SubAgent({ node, ctx }: { node: TreeNode; ctx: RenderCtx }) {
  const [open, setOpen] = useState(true);
  const id = useId();
  const stats = agentStats(node);
  const tokens = stats.usage.inputTokens + stats.usage.outputTokens;
  const title = `Sub-agent${node.event.text ? `: ${node.event.text}` : ""}${node.orphan ? " (detached)" : ""}`;
  return (
    <div className="rounded border border-accent/50 px-3 py-2">
      <button
        type="button"
        className="flex w-full items-baseline gap-2 text-left text-sm font-medium"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => {
          setOpen((o) => !o);
        }}
      >
        <span aria-hidden="true" className="text-muted">
          {open ? "▾" : "▸"}
        </span>
        <span>{title}</span>
      </button>
      <p className="mt-1 text-xs text-muted">
        {stats.toolCalls} tool call{stats.toolCalls === 1 ? "" : "s"}
        {stats.durationMs === undefined ? "" : ` · ${formatDuration(stats.durationMs)}`}
        {tokens > 0 ? ` · ${formatTokens(tokens)} tokens` : ""}
        {ctx.capabilities?.costReporting !== false && stats.usage.costUsd !== undefined ? ` · ${formatCost(stats.usage.costUsd)}` : ""}
      </p>
      <div id={id} hidden={!open}>
        {open ? <NodeList nodes={node.children} ctx={ctx} /> : null}
      </div>
    </div>
  );
}

function NodeList({ nodes, ctx }: { nodes: TreeNode[]; ctx: RenderCtx }) {
  return (
    <ol className="mt-2 space-y-2">
      {nodes.map((n) => {
        const e = n.event;
        // Usage is summed in the header, a sub-agent's end is shown by its stats,
        // and a result with a call shows inside that call.
        if (e.kind === EventUsage || e.kind === EventSubagentEnd) return null;
        if (e.kind === EventToolResult && e.resultFor && ctx.results.get(e.resultFor) === e && ctx.callIds.has(e.resultFor)) {
          return null;
        }
        return (
          <li key={e.seq}>{e.kind === EventSubagentStart ? <SubAgent node={n} ctx={ctx} /> : <EventView event={e} ctx={ctx} />}</li>
        );
      })}
    </ol>
  );
}

// ---- tooltip --------------------------------------------------------------

/** A greyed (still focusable) note with the reason in a tooltip. */
function Greyed({ label, reason }: { label: string; reason: string }) {
  return (
    <Tooltip.Provider delayDuration={200}>
      <Tooltip.Root>
        <Tooltip.Trigger asChild>
          <span tabIndex={0} aria-disabled="true" aria-label={`${label}. ${reason}`} className="cursor-not-allowed opacity-60">
            {label}
          </span>
        </Tooltip.Trigger>
        <Tooltip.Portal>
          <Tooltip.Content className="z-50 max-w-xs rounded border border-line bg-surface px-2 py-1 text-xs shadow" sideOffset={4}>
            {reason}
          </Tooltip.Content>
        </Tooltip.Portal>
      </Tooltip.Root>
    </Tooltip.Provider>
  );
}

export function UsageSummary({ usage, capabilities }: { usage: UsageTotals; capabilities?: Capabilities }) {
  const tokens = usage.inputTokens + usage.outputTokens;
  const costKnown = capabilities?.costReporting !== false;
  if (tokens === 0 && usage.costUsd === undefined && usage.turns === 0 && costKnown) return null;
  return (
    <p className="text-sm text-muted">
      {formatTokens(tokens)} tokens
      {costKnown && usage.costUsd !== undefined && usage.costUsd > 0 ? ` · ${formatCost(usage.costUsd)}` : ""}
      {costKnown ? null : (
        <>
          {" · "}
          <Greyed label="Cost not reported" reason="This agent does not report what a run costs, so only tokens are counted." />
        </>
      )}
      {usage.turns > 0 ? ` · ${String(usage.turns)} turn${usage.turns === 1 ? "" : "s"}` : ""}
    </p>
  );
}

/** Resume action: greyed with a reason when the agent can't resume a session. */
export function ResumeButton({ capabilities, onResume }: { capabilities?: Capabilities; onResume: () => void }) {
  if (capabilities?.resume === false) {
    return (
      <Tooltip.Provider delayDuration={200}>
        <Tooltip.Root>
          <Tooltip.Trigger asChild>
            <button type="button" aria-disabled="true" className="cursor-not-allowed rounded border border-line px-2 py-0.5 text-sm opacity-60">
              Resume
            </button>
          </Tooltip.Trigger>
          <Tooltip.Portal>
            <Tooltip.Content className="z-50 max-w-xs rounded border border-line bg-surface px-2 py-1 text-xs shadow" sideOffset={4}>
              This agent can't continue an earlier session. Start a new task instead.
            </Tooltip.Content>
          </Tooltip.Portal>
        </Tooltip.Root>
      </Tooltip.Provider>
    );
  }
  return (
    <button type="button" className="rounded border border-line px-2 py-0.5 text-sm hover:bg-surface" onClick={onResume}>
      Resume
    </button>
  );
}

// ---- the transcript -------------------------------------------------------

/** One line about the run: state, tool work and elapsed time. */
export function StatusLine({ events, label }: { events: readonly AgentEvent[]; label?: string }) {
  const parts: string[] = [];
  if (label) parts.push(label);
  const calls = events.filter((e) => e.kind === EventToolCall || e.kind === EventCommand || e.kind === EventFileEdit).length;
  parts.push(`${String(calls)} tool call${calls === 1 ? "" : "s"}`);
  const first = events[0] ? Date.parse(events[0].at) : NaN;
  const last = events.length > 0 ? Date.parse(events[events.length - 1]?.at ?? "") : NaN;
  if (!Number.isNaN(first) && !Number.isNaN(last) && last > first) parts.push(formatDuration(last - first));
  return (
    <p role="status" className="text-xs text-muted">
      {parts.join(" · ")}
    </p>
  );
}

/** The transcript of a task: messages, tool calls, edits and a tree of sub-agents. */
export function Transcript({ events, capabilities }: { events: readonly AgentEvent[]; capabilities?: Capabilities }) {
  const [extra, setExtra] = useState(0);
  const nested = capabilities === undefined || capabilities.subagents === SubagentsNested;
  const roots = useMemo(() => {
    if (nested) return buildTree(events);
    // No parent links to trust: show everything in order, flat.
    return [...events].sort((a, b) => a.seq - b.seq).map((event): TreeNode => ({ event, children: [], orphan: false }));
  }, [events, nested]);
  const ctx = useMemo<RenderCtx>(() => {
    const results = new Map<string, AgentEvent>();
    const callIds = new Set<string>();
    for (const e of events) {
      if (e.kind === EventToolCall && e.id) callIds.add(e.id);
    }
    for (const e of [...events].sort((a, b) => a.seq - b.seq)) {
      if (e.kind === EventToolResult && e.resultFor && !results.has(e.resultFor)) results.set(e.resultFor, e);
    }
    return { results, callIds, capabilities };
  }, [events, capabilities]);

  if (roots.length === 0) return <p className="text-sm text-muted">Nothing has happened yet.</p>;

  const limit = VISIBLE_ROOTS + extra;
  const hiddenCount = Math.max(0, roots.length - limit);
  const visible = hiddenCount > 0 ? roots.slice(hiddenCount) : roots;
  const turns = groupTurns(visible);
  const hasSub = events.some((e) => e.kind === EventSubagentStart);

  return (
    <section aria-label="Transcript" className="space-y-3">
      {!nested && hasSub ? (
        <p className="text-xs text-muted">This agent doesn't link sub-agent activity to its parent, so everything is shown in order.</p>
      ) : null}
      {hiddenCount > 0 ? (
        <button
          type="button"
          className="text-sm text-accent underline"
          onClick={() => {
            setExtra((x) => x + VISIBLE_ROOTS);
          }}
        >
          Show earlier events ({String(hiddenCount)})
        </button>
      ) : null}
      {turns.map((turn, i) => (
        <div key={turn.nodes[0]?.event.seq ?? i}>
          {turn.durationMs !== undefined ? (
            <p className="text-xs text-muted">Turn took {formatDuration(turn.durationMs)}</p>
          ) : null}
          <NodeList nodes={turn.nodes} ctx={ctx} />
        </div>
      ))}
    </section>
  );
}
