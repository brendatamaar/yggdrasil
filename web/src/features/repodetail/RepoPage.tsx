import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";
import { ApiError, describeError } from "../../api/client";
import { relativeTime } from "../repos/ui";
import { BranchesTab } from "./BranchesTab";
import { ChangesTab } from "./ChangesTab";
import { HistoryTab } from "./HistoryTab";
import { HostBlock } from "./HostBlock";
import { useRepoDetailLive, useRepoStatus } from "./hooks";
import { Notice, Tabs } from "./parts";

const tabs = [
  { id: "changes", label: "Changes" },
  { id: "history", label: "History" },
  { id: "branches", label: "Branches" },
];

function NotTracked() {
  return (
    <div className="space-y-2 p-4">
      <h1 className="text-lg font-semibold">This repo isn't tracked</h1>
      <p className="text-sm text-muted">It may have been removed from its project.</p>
      <Link to="/projects" className="text-sm text-accent underline">
        Back to projects
      </Link>
    </div>
  );
}

export function RepoPage() {
  const { repoId } = useParams({ strict: false });
  return repoId ? <RepoView id={repoId} /> : <NotTracked />;
}

function RepoView({ id }: { id: string }) {
  useRepoDetailLive(id);
  const [tab, setTab] = useState("changes");
  const q = useRepoStatus(id);

  if (q.isPending) return <p className="p-4 text-sm text-muted">Loading repo…</p>;
  if (q.isError) {
    if (q.error instanceof ApiError && q.error.status === 404) return <NotTracked />;
    return (
      <div className="p-4">
        <Notice tone="danger">{describeError(q.error)}</Notice>
      </div>
    );
  }
  if (!q.data) return <NotTracked />;
  const { repo, projectName } = q.data;

  return (
    <div className="space-y-4 p-4">
      <header className="space-y-1">
        <p className="text-sm text-muted">
          <Link to="/projects" className="underline">
            Projects
          </Link>{" "}
          / {projectName}
        </p>
        <h1 className="text-xl font-semibold">{repo.name}</h1>
        <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
          <span>
            Branch: <span className="font-mono">{repo.detached ? "detached" : repo.branch || "-"}</span>
          </span>
          {repo.hasUpstream ? (
            <span>
              {repo.ahead} ahead / {repo.behind} behind
            </span>
          ) : (
            <span className="text-muted">No upstream</span>
          )}
          {repo.flags.map((f) => (
            <span key={f} className="rounded-sm border border-line px-1 text-xs text-warn">
              {f.replace(/_/g, " ")}
            </span>
          ))}
        </p>
        {repo.lastCommit && (
          <p className="text-sm text-muted">
            Last commit: {repo.lastCommit.subject} · {repo.lastCommit.author} · {relativeTime(repo.lastCommit.at)}
          </p>
        )}
      </header>

      {/* Room for git actions (a later task). */}
      <div role="toolbar" aria-label="Repo actions" data-testid="repo-actions" className="flex min-h-0 gap-2 empty:hidden" />

      <HostBlock repoId={id} />

      <div>
        <Tabs tabs={tabs} value={tab} onChange={setTab} label="Repo sections" />
        <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`} className="pt-3">
          {tab === "changes" && <ChangesTab repoId={id} />}
          {tab === "history" && <HistoryTab repoId={id} />}
          {tab === "branches" && (
            <BranchesTab repoId={id} ahead={repo.ahead} behind={repo.behind} hasUpstream={repo.hasUpstream} />
          )}
        </div>
      </div>
    </div>
  );
}
