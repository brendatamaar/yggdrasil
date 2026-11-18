import { useMutation } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { describeError } from "../../api/client";
import type { StandupResponse } from "../../api/gen/api.gen";
import { EmptyNote } from "../actions/EmptyNote";
import { newRequestId } from "../actions/hooks";
import { pickAgent, useAgents } from "../composer/agents";
import { useRepos } from "../repos/hooks";
import { fetchStandup, polishStandup, StandupDecodeError } from "./api";
import {
  dayString,
  isEmptySummary,
  MAX_RANGE_DAYS,
  rangeFromDays,
  stepStandup,
  type StandupCommand,
  type StandupInput,
  type StandupState,
} from "./core";

const SOURCE_LABEL: Record<string, string> = {
  commits: "Commits (from git history)",
  merge_requests: "Merge requests (from the host cache)",
  mrs: "Merge requests (from the host cache)",
  builds: "Builds (from local build history)",
};
const sourceLabel = (s: string): string => SOURCE_LABEL[s] ?? s;

const btn = "rounded border border-line px-3 py-1 text-sm";
const primary = "rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50";

/** Standup of one project over a day range (AU-02, UI-03). Nothing is read until Refresh, and reading never starts an agent. */
export function StandupPanel() {
  const repos = useRepos("all");
  const [chosen, setChosen] = useState("");
  if (repos.isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (repos.error) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(repos.error)}
      </p>
    );
  }
  const projects = repos.data.projects;
  if (projects.length === 0) return <EmptyNote kind="NoProjects" />;
  const project = projects.find((p) => p.id === chosen) ?? projects[0];
  if (!project) return <EmptyNote kind="NoProjects" />;
  return (
    <div className="space-y-2">
      {projects.length > 1 ? (
        <label className="flex items-center gap-2 text-sm">
          Project
          <select
            className="rounded border border-line bg-canvas px-2 py-1"
            value={project.id}
            onChange={(e) => {
              setChosen(e.target.value);
            }}
          >
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <p className="text-sm text-muted">{project.name}</p>
      )}
      <StandupView key={project.id} projectId={project.id} />
    </div>
  );
}

function StandupView({ projectId }: { projectId: string }) {
  const today = dayString(new Date());
  const [fromDay, setFromDay] = useState(today);
  const [toDay, setToDay] = useState(today);
  const [state, setState] = useState<StandupState>({ tag: "empty" });
  const stateRef = useRef<StandupState>(state);
  const attemptRef = useRef(0);
  const abort = useRef<AbortController | null>(null);
  const dispatchRef = useRef<(i: StandupInput) => void>(() => undefined);
  const agents = useAgents();
  const agent = pickAgent(agents.data ?? [], "");
  const polishKey = useRef<{ sig: string; id: string } | null>(null);
  const polish = useMutation({
    mutationFn: (v: { from: string; to: string; requestId: string }) =>
      polishStandup(projectId, { requestId: v.requestId, from: v.from, to: v.to, agent }),
  });

  const run = useCallback(
    (c: StandupCommand) => {
      switch (c.tag) {
        case "readSources": {
          const ctl = new AbortController();
          abort.current = ctl;
          fetchStandup(projectId, c.range.from, c.range.to, ctl.signal)
            .then((response) => {
              dispatchRef.current({ tag: "sourcesRead", attempt: c.attempt, response });
            })
            .catch((err: unknown) => {
              if (ctl.signal.aborted) return;
              dispatchRef.current({
                tag: "readFailed",
                attempt: c.attempt,
                reason: err instanceof StandupDecodeError ? err.message : describeError(err),
              });
            });
          return;
        }
        case "cancelRead":
          abort.current?.abort();
          return;
        case "submitPolish":
          polish.mutate({ from: c.range.from, to: c.range.to, requestId: c.requestId });
          return;
      }
    },
    // `polish.mutate` is stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [projectId, polish.mutate],
  );

  const dispatch = useCallback(
    (input: StandupInput) => {
      const [next, cmds] = stepStandup(stateRef.current, input);
      stateRef.current = next;
      setState(next);
      cmds.forEach(run);
    },
    [run],
  );
  useEffect(() => {
    dispatchRef.current = dispatch;
  }, [dispatch]);

  const [rangeError, setRangeError] = useState("");
  const refresh = useCallback(() => {
    const r = rangeFromDays(fromDay, toDay);
    if (!r.ok) {
      setRangeError(r.message);
      return;
    }
    setRangeError("");
    attemptRef.current += 1;
    dispatch({ tag: "requested", range: r.range, attempt: attemptRef.current });
  }, [fromDay, toDay, dispatch]);

  useEffect(
    () => () => {
      abort.current?.abort();
    },
    [],
  );

  const summary = state.tag === "ready" || state.tag === "partial" ? state.summary : undefined;
  const onPolish = () => {
    if (!summary) return;
    const sig = `${summary.from}|${summary.to}|${summary.text}`;
    if (polishKey.current?.sig !== sig) polishKey.current = { sig, id: newRequestId() };
    dispatch({ tag: "polishRequested", requestId: polishKey.current.id });
  };

  return (
    <div className="space-y-3 text-sm">
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          refresh();
        }}
      >
        <label className="grid gap-1">
          <span className="text-xs text-muted">First day</span>
          <input
            type="date"
            value={fromDay}
            className="rounded border border-line bg-canvas px-2 py-1"
            onChange={(e) => {
              setFromDay(e.target.value);
            }}
          />
        </label>
        <label className="grid gap-1">
          <span className="text-xs text-muted">Last day</span>
          <input
            type="date"
            value={toDay}
            className="rounded border border-line bg-canvas px-2 py-1"
            onChange={(e) => {
              setToDay(e.target.value);
            }}
          />
        </label>
        <button type="submit" className={btn} disabled={state.tag === "loading"}>
          Refresh
        </button>
        <span className="text-xs text-muted">At most {MAX_RANGE_DAYS} days. Refresh only reads; it never starts an agent.</span>
      </form>
      {rangeError ? (
        <p role="alert" className="text-danger">
          {rangeError}
        </p>
      ) : null}
      {state.tag === "empty" && !rangeError ? <p className="text-muted">Pick a range, then press Refresh. Nothing is read until you do.</p> : null}
      {state.tag === "loading" ? (
        <p role="status" className="text-muted">
          Reading sources…
        </p>
      ) : null}
      {state.tag === "failed" ? (
        <div role="alert" className="space-y-1 text-danger">
          <p>{state.reason}</p>
          <button type="button" className={btn} onClick={refresh}>
            Try again
          </button>
        </div>
      ) : null}
      {summary ? (
        <Summary
          summary={summary}
          partial={state.tag === "partial"}
          onPolish={onPolish}
          polishing={polish.isPending}
          polishDone={polish.data}
          polishError={polish.error ? describeError(polish.error) : ""}
          noAgent={agent === ""}
        />
      ) : null}
    </div>
  );
}

function Summary({
  summary,
  partial,
  onPolish,
  polishing,
  polishDone,
  polishError,
  noAgent,
}: {
  summary: StandupResponse;
  partial: boolean;
  onPolish: () => void;
  polishing: boolean;
  polishDone: { message: string; taskId?: string } | undefined;
  polishError: string;
  noAgent: boolean;
}) {
  const [copied, setCopied] = useState<"" | "done" | "failed">("");
  const empty = isEmptySummary(summary);
  const copy = () => {
    // User gesture only; if the clipboard refuses, the text stays selectable.
    navigator.clipboard.writeText(summary.text).then(
      () => {
        setCopied("done");
      },
      () => {
        setCopied("failed");
      },
    );
  };
  return (
    <div className="space-y-2">
      {partial ? (
        <div data-standup="partial" className="space-y-1 text-warn">
          <p>Partial: some sources could not be read.</p>
          <ul aria-label="Unavailable sources" className="list-disc pl-5 text-xs">
            {(summary.unavailable ?? []).map((u) => (
              <li key={`${u.source}:${u.repoId ?? ""}`}>
                {sourceLabel(u.source)}
                {u.repoId ? ` (${u.repoId})` : ""}: {u.reason}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {(summary.truncated ?? []).length > 0 ? (
        <p data-standup="truncated" className="text-xs text-warn">
          Cut to fit the limit: {(summary.truncated ?? []).map(sourceLabel).join(", ")}.
        </p>
      ) : null}
      {empty ? (
        <EmptyNote kind="NoStandup" />
      ) : (
        <>
          <p className="text-xs text-muted">
            {summary.commits.length} commits (from git history), {summary.mrs.length} merge requests (from the host cache),{" "}
            {summary.builds.length} builds (from local build history).
          </p>
          <pre
            tabIndex={0}
            aria-label="Standup summary"
            className="max-h-64 overflow-auto rounded border border-line bg-surface p-2 font-mono text-xs break-words whitespace-pre-wrap"
          >
            {summary.text}
          </pre>
        </>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <button type="button" className={btn} disabled={empty} onClick={copy}>
          Copy
        </button>
        <button type="button" className={primary} disabled={empty || polishing || noAgent} onClick={onPolish}>
          Polish with agent
        </button>
        <span role="status" className="text-xs text-muted">
          {copied === "done" ? "Copied." : copied === "failed" ? "Copy was blocked. Select the text and copy it yourself." : ""}
        </span>
      </div>
      <p className="text-xs text-muted">Polish starts one visible Ask task with this text. Nothing is sent until you press it.</p>
      {polishError ? (
        <p role="alert" className="text-danger">
          {polishError}
        </p>
      ) : null}
      {polishDone ? (
        <p role="status">
          {polishDone.message}{" "}
          {polishDone.taskId ? (
            <Link to="/tasks/$taskId" params={{ taskId: polishDone.taskId }} className="text-accent underline">
              Open the task
            </Link>
          ) : null}
        </p>
      ) : null}
    </div>
  );
}
