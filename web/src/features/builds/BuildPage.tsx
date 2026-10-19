import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { cancelBuild } from "../../api/builds";
import { describeError } from "../../api/client";
import { useRepos } from "../repos/hooks";
import { btn } from "../repos/ui";
import { useBuild, useBuildMutation, useBuildsLive } from "./hooks";
import { LogViewer } from "./LogViewer";
import { buildLabel, formatDuration, formatSize, isActive, statusTone, stepOutcomeText } from "./ui";

export function BuildPage({ buildId }: { buildId: string }) {
  const build = useBuild(buildId);
  const repos = useRepos("all");
  useBuildsLive(buildId);
  const cancel = useBuildMutation(cancelBuild);
  const [picked, setPicked] = useState<number | null>(null);

  const b = build.data;
  if (build.isError) {
    return <p role="alert" className="p-4 text-sm text-red-500">{describeError(build.error)}</p>;
  }
  if (!b) return <p className="p-4 text-sm text-muted">Loading build...</p>;

  const repoList = repos.data?.projects.flatMap((p) => p.repos) ?? [];
  const repoName = (id: string) => repoList.find((r) => r.id === id)?.name ?? id;
  const started = b.steps.filter((s) => s.status !== "queued" && s.status !== "skipped");
  const auto =
    b.steps.find((s) => s.status === "running") ?? b.steps.find((s) => s.status === "failed") ?? started.at(-1);
  const sel = (picked === null ? undefined : b.steps[picked]) ?? auto;

  return (
    <div className="flex flex-col gap-4 p-4">
      <div className="flex items-center gap-3">
        <Link to="/builds" className="text-sm text-muted underline">
          Builds
        </Link>
        <h1 className="text-lg font-semibold">
          Build <span className={statusTone(b.status)}>{buildLabel(b)}</span>
        </h1>
        <span className="text-sm text-muted">{formatDuration(b.startedAt, b.finishedAt)}</span>
        {isActive(b) && (
          <button
            type="button"
            className={`${btn} ml-auto`}
            disabled={cancel.isPending || buildLabel(b) === "Cancelling"}
            onClick={() => {
              cancel.mutate(b.id);
            }}
          >
            Cancel build
          </button>
        )}
      </div>
      {cancel.isError && <p role="alert" className="text-sm text-red-500">{describeError(cancel.error)}</p>}

      <ol className="flex flex-col gap-1" aria-label="Steps">
        {b.steps.map((s) => (
          <li key={s.index}>
            <button
              type="button"
              aria-pressed={sel?.index === s.index}
              onClick={() => {
                setPicked(s.index);
              }}
              className={`flex w-full flex-wrap items-baseline gap-2 rounded-md border px-2 py-1 text-left text-sm ${sel?.index === s.index ? "border-accent" : "border-line"}`}
            >
              <span className="font-medium">{s.name}</span>
              <span className="text-muted">{repoName(s.repoId)}</span>
              <span className={statusTone(s.outcome ?? s.status)}>{stepOutcomeText(s)}</span>
              {s.logTruncated && <span className="text-muted">Log cut off</span>}
              <code className="ml-auto text-xs text-muted">{s.argv.join(" ")}</code>
            </button>
          </li>
        ))}
      </ol>

      {sel && sel.status !== "queued" && sel.status !== "skipped" && (
        <LogViewer key={`${String(sel.index)}-${String(sel.attempt)}`} buildId={b.id} step={sel.index} attempt={sel.attempt} />
      )}

      {b.status === "succeeded" && (
        <section aria-labelledby="art-h">
          <h2 id="art-h" className="font-semibold">Artifacts</h2>
          {b.artifacts.length === 0 ? (
            <p className="text-sm text-muted">This build produced no artifacts.</p>
          ) : (
            <ul className="text-sm">
              {b.artifacts.map((a) => (
                <li key={`${a.repoId}/${a.path}`} className="flex items-center gap-3">
                  <span className="text-muted">{repoName(a.repoId)}</span>
                  <code>{a.path}</code>
                  <span>{formatSize(a.size)}</span>
                  <button type="button" className={btn} disabled title="Available in a later version">
                    Show in folder
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}
    </div>
  );
}
