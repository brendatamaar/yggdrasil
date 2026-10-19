import * as Dialog from "@radix-ui/react-dialog";
import * as Menu from "@radix-ui/react-dropdown-menu";
import { useMutation } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { describeError } from "../../api/client";
import type { ProjectDTO } from "../../api/gen/api.gen";
import { renameProject, rescanProject, trackRepos, untrackProject, untrackRepo } from "../../api/repos";
import { CandidateList, DialogShell } from "./AddProjectDialog";
import { useRepoMutation } from "./hooks";
import { btn, btnPrimary, menuContent, menuItem, useChecked } from "./ui";

type Mode = "rescan" | "untrack" | "rename" | null;

function Err({ error }: { error: unknown }) {
  return (
    <p role="alert" className="text-sm text-danger">
      {describeError(error)}
    </p>
  );
}

function Rescan({ project, onClose }: { project: ProjectDTO; onClose: () => void }) {
  const scan = useMutation({ mutationFn: () => rescanProject(project.id) });
  const track = useRepoMutation((picks: { relPath: string; name: string }[]) => trackRepos(project.id, picks));
  const stop = useRepoMutation(untrackRepo);
  const { checked, toggle } = useChecked();
  const { mutate: runScan } = scan;
  useEffect(() => {
    runScan();
  }, [runScan]);
  const res = scan.data;
  const set = res ? checked(res.new) : new Set<string>();
  const nameOf = (id: string) => project.repos.find((r) => r.id === id)?.name ?? id;

  return (
    <>
      {scan.isPending && <p className="text-sm text-muted">Scanning...</p>}
      {scan.isError && <Err error={scan.error} />}
      {res && res.new.length === 0 && res.missing.length === 0 && (
        <p className="text-sm">Nothing new and nothing missing.</p>
      )}
      {res && res.new.length > 0 && (
        <>
          <h3 className="text-sm font-medium">New repos</h3>
          <CandidateList candidates={res.new} checked={set} onToggle={toggle} />
          <div>
            <button
              type="button"
              className={btnPrimary}
              disabled={track.isPending || set.size === 0}
              onClick={() => {
                track.mutate(
                  res.new.filter((c) => set.has(c.relPath)).map((c) => ({ relPath: c.relPath, name: c.name })),
                  { onSuccess: onClose },
                );
              }}
            >
              Track selected
            </button>
          </div>
          {track.isError && <Err error={track.error} />}
        </>
      )}
      {res && res.missing.length > 0 && (
        <>
          <h3 className="text-sm font-medium">Missing from disk</h3>
          <ul>
            {res.missing.map((id) => (
              <li key={id} className="flex items-center justify-between py-1 text-sm">
                {nameOf(id)}
                <button
                  type="button"
                  className={btn}
                  disabled={stop.isPending}
                  onClick={() => {
                    stop.mutate(id);
                  }}
                >
                  Stop tracking {nameOf(id)}
                </button>
              </li>
            ))}
          </ul>
          {stop.isError && <Err error={stop.error} />}
        </>
      )}
      <div className="flex justify-end">
        <Dialog.Close className={btn}>Close</Dialog.Close>
      </div>
    </>
  );
}

function Untrack({ project, onClose }: { project: ProjectDTO; onClose: () => void }) {
  const m = useRepoMutation(untrackProject);
  return (
    <>
      {m.isError && <Err error={m.error} />}
      <div className="flex justify-end gap-2">
        <Dialog.Close className={btn}>Cancel</Dialog.Close>
        <button
          type="button"
          className={btnPrimary}
          disabled={m.isPending}
          onClick={() => {
            m.mutate(project.id, { onSuccess: onClose });
          }}
        >
          Stop tracking project
        </button>
      </div>
    </>
  );
}

function Rename({ project, onClose }: { project: ProjectDTO; onClose: () => void }) {
  const [name, setName] = useState(project.name);
  const m = useRepoMutation((n: string) => renameProject(project.id, n));
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        m.mutate(name.trim(), { onSuccess: onClose });
      }}
    >
      <label className="flex flex-col gap-1 text-sm">
        Project name
        <input
          className="rounded-md border border-line bg-canvas px-2 py-1"
          value={name}
          onChange={(e) => {
            setName(e.target.value);
          }}
        />
      </label>
      {m.isError && <Err error={m.error} />}
      <div className="flex justify-end gap-2">
        <Dialog.Close className={btn}>Cancel</Dialog.Close>
        <button type="submit" className={btnPrimary} disabled={m.isPending || name.trim() === ""}>
          Rename
        </button>
      </div>
    </form>
  );
}

const titles = {
  rescan: ["Rescan project", "Looks for repos added to or removed from the folder."],
  untrack: ["Stop tracking this project?", "Nothing on disk is touched. Tracking the folder again brings its history back."],
  rename: ["Rename project", "Only the name in Yggdrasil changes."],
} as const;

export function ProjectMenu({ project }: { project: ProjectDTO }) {
  const [mode, setMode] = useState<Mode>(null);
  const close = () => {
    setMode(null);
  };
  const item = (m: Exclude<Mode, null>, label: string) => (
    <Menu.Item
      className={menuItem}
      onSelect={() => {
        setMode(m);
      }}
    >
      {label}
    </Menu.Item>
  );
  return (
    <>
      <Menu.Root>
        <Menu.Trigger aria-label={`Actions for project ${project.name}`} className={btn}>
          ...
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Content align="end" className={menuContent}>
            {item("rescan", "Rescan")}
            {item("rename", "Rename")}
            {item("untrack", "Stop tracking project")}
          </Menu.Content>
        </Menu.Portal>
      </Menu.Root>
      <Dialog.Root
        open={mode !== null}
        onOpenChange={(o) => {
          if (!o) close();
        }}
      >
        {mode && (
          <DialogShell title={titles[mode][0]} description={titles[mode][1]}>
            {mode === "rescan" && <Rescan project={project} onClose={close} />}
            {mode === "untrack" && <Untrack project={project} onClose={close} />}
            {mode === "rename" && <Rename project={project} onClose={close} />}
          </DialogShell>
        )}
      </Dialog.Root>
    </>
  );
}
