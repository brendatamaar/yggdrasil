import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { runPreset } from "../../api/builds";
import { describeError } from "../../api/client";
import type { BuildDTO } from "../../api/gen/api.gen";
import { useRepos } from "../repos/hooks";
import { btn, btnPrimary, relativeTime } from "../repos/ui";
import { useBuildMutation, useBuilds, useBuildsLive, usePresets } from "./hooks";
import { NewBuildDialog } from "./NewBuildDialog";
import { PresetDialog } from "./PresetDialog";
import { buildLabel, formatDuration, input, statusTone } from "./ui";

const statuses = ["queued", "running", "succeeded", "failed", "cancelled", "interrupted"];

export function BuildsPage() {
  const [project, setProject] = useState("");
  const [status, setStatus] = useState("");
  const navigate = useNavigate();
  const builds = useBuilds({ project, status });
  const presets = usePresets();
  const repos = useRepos("all");
  useBuildsLive();
  const run = useBuildMutation(runPreset);

  const projects = repos.data?.projects ?? [];
  const repoList = projects.flatMap((p) => p.repos);
  const repoName = (id: string) => repoList.find((r) => r.id === id)?.name ?? id;
  const go = (id: string) => {
    void navigate({ to: "/builds/$buildId", params: { buildId: id } });
  };
  const repoNames = (b: BuildDTO) => [...new Set(b.steps.map((s) => repoName(s.repoId)))].join(", ");

  return (
    <div className="flex flex-col gap-6 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-lg font-semibold">Builds</h1>
        <label className="flex items-center gap-1 text-sm">
          Project
          <select className={input} value={project} onChange={(e) => { setProject(e.target.value); }}>
            <option value="">All</option>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-1 text-sm">
          Status
          <select className={input} value={status} onChange={(e) => { setStatus(e.target.value); }}>
            <option value="">All</option>
            {statuses.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <div className="ml-auto">
          <NewBuildDialog
            repos={repoList}
            presets={presets.data ?? []}
            onStarted={go}
            trigger={<button type="button" className={btnPrimary}>New build</button>}
          />
        </div>
      </div>

      {builds.isError && <p role="alert" className="text-sm text-red-500">{describeError(builds.error)}</p>}
      {builds.data?.length === 0 && <p className="text-sm text-muted">No builds yet. Start one with New build.</p>}
      {builds.data && builds.data.length > 0 && (
        <table className="w-full text-left text-sm">
          <thead className="text-muted">
            <tr>
              <th className="py-1 pr-3">Status</th>
              <th className="pr-3">Repos</th>
              <th className="pr-3">Started</th>
              <th className="pr-3">Finished</th>
              <th>Duration</th>
            </tr>
          </thead>
          <tbody>
            {builds.data.map((b) => (
              <tr key={b.id} className="border-t border-line">
                <td className={`py-1 pr-3 ${statusTone(b.status)}`}>
                  <Link to="/builds/$buildId" params={{ buildId: b.id }} className="underline">
                    {buildLabel(b)}
                  </Link>
                </td>
                <td className="pr-3">{repoNames(b)}</td>
                <td className="pr-3">{b.startedAt ? relativeTime(b.startedAt) : "Not started"}</td>
                <td className="pr-3">{b.finishedAt ? relativeTime(b.finishedAt) : "-"}</td>
                <td>{formatDuration(b.startedAt, b.finishedAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <section aria-labelledby="presets-h" className="flex flex-col gap-2">
        <div className="flex items-center gap-3">
          <h2 id="presets-h" className="font-semibold">Presets</h2>
          <PresetDialog
            projects={projects}
            trigger={<button type="button" className={btn}>New preset</button>}
          />
        </div>
        {run.isError && <p role="alert" className="text-sm text-red-500">{describeError(run.error)}</p>}
        {presets.data?.length === 0 && <p className="text-sm text-muted">No presets yet.</p>}
        <ul className="flex flex-col gap-1">
          {presets.data?.map((p) => (
            <li key={p.id} className="flex items-center gap-2 text-sm">
              <span className="font-medium">{p.name}</span>
              <span className="text-muted">{String(p.steps.length)} steps</span>
              <button
                type="button"
                className={btn}
                aria-label={`Run ${p.name}`}
                onClick={() => {
                  run.mutate(p.id, { onSuccess: (r) => { go(r.buildId); } });
                }}
              >
                Run
              </button>
              <PresetDialog
                projects={projects}
                preset={p}
                trigger={<button type="button" className={btn} aria-label={`Edit ${p.name}`}>Edit</button>}
              />
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}
