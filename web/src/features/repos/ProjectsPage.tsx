import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { describeError } from "../../api/client";
import type { RepoCountsDTO } from "../../api/gen/api.gen";
import { refreshRepos, type RepoFilter } from "../../api/repos";
import { AddProjectDialog } from "./AddProjectDialog";
import { useRefreshOnFocus, useRepos, useReposLive } from "./hooks";
import { ProjectMenu } from "./ProjectMenu";
import { RepoTable } from "./RepoTable";
import { btn, btnPrimary } from "./ui";

const filters: { id: RepoFilter; label: string; key: keyof RepoCountsDTO }[] = [
  { id: "all", label: "All", key: "all" },
  { id: "uncommitted", label: "Uncommitted", key: "uncommitted" },
  { id: "behind", label: "Behind", key: "behind" },
  { id: "ahead", label: "Ahead", key: "ahead" },
  { id: "failed_pipeline", label: "Failed pipeline", key: "failedPipeline" },
  { id: "problems", label: "Problems", key: "problems" },
];

type Tab = "repos" | "builds" | "services";

export function ProjectsPage() {
  const [tab, setTab] = useState<Tab>("repos");
  const [filter, setFilter] = useState<RepoFilter>("all");
  const repos = useRepos(filter);
  const refresh = useMutation({ mutationFn: () => refreshRepos({ ids: [], reason: "user" }) });
  const projects = repos.data?.projects ?? [];
  useReposLive(projects.flatMap((p) => p.repos.map((r) => r.id)));
  useRefreshOnFocus();

  const counts = repos.data?.counts;
  const total = counts?.all ?? 0;
  const empty = repos.isSuccess && projects.length === 0 && filter === "all";
  const add = <button type="button" className={btnPrimary}>Add project</button>;

  const tabs: [Tab, string, string][] = [
    ["repos", "Repos", String(total)],
    ["builds", "Builds", "0"],
    ["services", "Services", "0"],
  ];

  return (
    <div className="flex flex-col gap-3 p-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Projects</h1>
        <div className="flex gap-2">
          <button
            type="button"
            className={btn}
            disabled={refresh.isPending}
            onClick={() => {
              refresh.mutate();
            }}
          >
            Refresh
          </button>
          <AddProjectDialog trigger={add} />
        </div>
      </div>

      <div role="tablist" aria-label="Project areas" className="flex gap-1 border-b border-line">
        {tabs.map(([id, label, n]) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={tab === id}
            className={`px-3 py-1 text-sm ${tab === id ? "border-b-2 border-accent" : "text-muted"}`}
            onClick={() => {
              setTab(id);
            }}
          >
            {label} ({n})
          </button>
        ))}
      </div>

      {refresh.isError && <p role="alert" className="text-sm text-danger">{describeError(refresh.error)}</p>}

      {tab !== "repos" && (
        <p className="text-sm text-muted">{tab === "builds" ? "Builds" : "Services"} are coming soon.</p>
      )}

      {tab === "repos" && (
        <>
          {repos.isError && <p role="alert" className="text-sm text-danger">{describeError(repos.error)}</p>}
          {empty ? (
            <div className="flex flex-col items-start gap-2 rounded-md border border-line p-4">
              <p className="text-sm">
                Nothing is tracked yet. Add a folder that holds your git repos to see their branches, changes and
                health here. Nothing on disk is changed.
              </p>
              <AddProjectDialog trigger={<button type="button" className={btnPrimary}>Add project</button>} />
            </div>
          ) : (
            <>
              <div role="group" aria-label="Filter repos" className="flex flex-wrap gap-1">
                {filters.map((f) => (
                  <button
                    key={f.id}
                    type="button"
                    aria-pressed={filter === f.id}
                    className={`${btn} ${filter === f.id ? "bg-surface" : ""}`}
                    onClick={() => {
                      setFilter(f.id);
                    }}
                  >
                    {f.label} ({counts?.[f.key] ?? 0})
                  </button>
                ))}
              </div>
              {projects.map((p) => (
                <section key={p.id} aria-label={p.name} className="rounded-md border border-line">
                  <header className="flex items-center justify-between px-3 py-2">
                    <h2 className="text-sm font-semibold">
                      {p.name} <span className="font-mono text-xs font-normal text-muted">{p.path}</span>
                    </h2>
                    <ProjectMenu project={p} />
                  </header>
                  <RepoTable repos={p.repos} />
                </section>
              ))}
            </>
          )}
        </>
      )}
    </div>
  );
}
