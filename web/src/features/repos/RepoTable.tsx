import * as Dialog from "@radix-ui/react-dialog";
import * as Menu from "@radix-ui/react-dropdown-menu";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { describeError } from "../../api/client";
import type { RepoStatusDTO } from "../../api/gen/api.gen";
import { untrackRepo } from "../../api/repos";
import { DialogShell } from "./AddProjectDialog";
import { useRepoMutation } from "./hooks";
import { btn, btnPrimary, menuContent, menuItem, relativeTime } from "./ui";

const flagInfo: Record<string, string> = {
  missing: "The folder is gone from disk.",
  not_a_repo: "The folder is no longer a git repo.",
  timed_out: "Reading this repo took too long. Refresh to try again.",
  git_failed: "Git could not read this repo. Refresh to try again.",
  detached: "Not on a branch (detached HEAD).",
  no_upstream: "The branch has no upstream to compare with.",
  conflicts: "There are merge conflicts to resolve.",
  diverged: "The branch has commits both ahead of and behind its upstream.",
};

function Badge({ flag }: { flag: string }) {
  const text = flagInfo[flag] ?? flag;
  return (
    <span title={text} aria-label={`${flag.replace(/_/g, " ")}: ${text}`} className="rounded-sm border border-line px-1 text-xs text-warn">
      {flag.replace(/_/g, " ")}
    </span>
  );
}

function Uncommitted({ r }: { r: RepoStatusDTO }) {
  const parts = [
    r.staged > 0 && `${String(r.staged)} staged`,
    r.unstaged > 0 && `${String(r.unstaged)} modified`,
    r.untracked > 0 && `${String(r.untracked)} untracked`,
  ].filter(Boolean);
  return <span>{parts.length ? parts.join(", ") : "clean"}</span>;
}

function RepoMenu({ repo }: { repo: RepoStatusDTO }) {
  const [confirm, setConfirm] = useState(false);
  const untrack = useRepoMutation(untrackRepo);
  return (
    <>
      <Menu.Root>
        <Menu.Trigger aria-label={`Actions for ${repo.name}`} className={btn}>
          ...
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Content align="end" className={menuContent}>
            <Menu.Item
              className={menuItem}
              onSelect={() => {
                setConfirm(true);
              }}
            >
              Stop tracking repo
            </Menu.Item>
          </Menu.Content>
        </Menu.Portal>
      </Menu.Root>
      <Dialog.Root
        open={confirm}
        onOpenChange={(o) => {
          setConfirm(o);
          if (!o) untrack.reset();
        }}
      >
        <DialogShell title={`Stop tracking ${repo.name}?`} description="Nothing on disk is touched. Tracking the folder again brings its history back.">
          {untrack.isError && (
            <p role="alert" className="text-sm text-danger">
              {describeError(untrack.error)}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Dialog.Close className={btn}>Cancel</Dialog.Close>
            <button
              type="button"
              className={btnPrimary}
              disabled={untrack.isPending}
              onClick={() => {
                untrack.mutate(repo.id, {
                  onSuccess: () => {
                    setConfirm(false);
                  },
                });
              }}
            >
              Stop tracking
            </button>
          </div>
        </DialogShell>
      </Dialog.Root>
    </>
  );
}

export function RepoTable({ repos }: { repos: RepoStatusDTO[] }) {
  if (repos.length === 0) return <p className="px-3 py-2 text-sm text-muted">No repos match.</p>;
  return (
    <table className="w-full text-left text-sm">
      <thead className="text-xs text-muted">
        <tr>
          <th className="px-3 py-1 font-normal">Repo</th>
          <th className="px-3 py-1 font-normal">Branch</th>
          <th className="px-3 py-1 font-normal">Uncommitted</th>
          <th className="px-3 py-1 font-normal">Ahead / behind</th>
          <th className="px-3 py-1 font-normal">Last commit</th>
          <th className="px-3 py-1 font-normal">Health</th>
          <th className="px-3 py-1 font-normal">
            <span className="sr-only">Actions</span>
          </th>
        </tr>
      </thead>
      <tbody>
        {repos.map((r) => (
          <tr key={r.id} className="border-t border-line">
            <td className="px-3 py-1 font-medium">
              <Link to="/repos/$repoId" params={{ repoId: r.id }} className="underline-offset-2 hover:underline">
                {r.name}
              </Link>
              {r.refreshing && (
                <span role="status" aria-label={`Refreshing ${r.name}`} className="ml-2 inline-block h-3 w-3 animate-spin rounded-full border-2 border-line border-t-accent" />
              )}
            </td>
            <td className="px-3 py-1">{r.detached ? "detached" : r.branch || "-"}</td>
            <td className="px-3 py-1">
              <Uncommitted r={r} />
            </td>
            <td className="px-3 py-1">
              {r.hasUpstream ? `${String(r.ahead)} ahead / ${String(r.behind)} behind` : "-"}
            </td>
            <td className="px-3 py-1">
              {r.lastCommit ? (
                <>
                  <span>{r.lastCommit.subject}</span>{" "}
                  <span className="text-xs text-muted">
                    {r.lastCommit.author}, {relativeTime(r.lastCommit.at)}
                  </span>
                </>
              ) : (
                "-"
              )}
            </td>
            <td className="space-x-1 px-3 py-1">
              {r.flags.map((f) => (
                <Badge key={f} flag={f} />
              ))}
            </td>
            <td className="px-3 py-1 text-right">
              <RepoMenu repo={r} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
