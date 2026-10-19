import * as Dialog from "@radix-ui/react-dialog";
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { describeError } from "../../api/client";
import type { CandidateDTO, PreviewResponse } from "../../api/gen/api.gen";
import { previewProject, trackProject } from "../../api/repos";
import { useDirs, useRepoMutation } from "./hooks";
import { btn, btnPrimary, useChecked } from "./ui";

export function DialogShell({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: React.ReactNode;
}) {
  return (
    <Dialog.Portal>
      <Dialog.Overlay className="fixed inset-0 bg-black/50" />
      <Dialog.Content className="fixed top-1/2 left-1/2 flex max-h-[85vh] w-[36rem] max-w-[95vw] -translate-x-1/2 -translate-y-1/2 flex-col gap-3 overflow-auto rounded-md border border-line bg-canvas p-4 shadow-lg">
        <Dialog.Title className="text-base font-semibold">{title}</Dialog.Title>
        <Dialog.Description className="text-sm text-muted">{description}</Dialog.Description>
        {children}
      </Dialog.Content>
    </Dialog.Portal>
  );
}

function lastSegment(path: string): string {
  const parts = path.split(/[\\/]+/).filter(Boolean);
  return parts.at(-1) ?? "";
}

function Browser({ path, onPath, onChoose }: { path: string; onPath: (p: string) => void; onChoose: (p: string) => void }) {
  const dirs = useDirs(path);
  const listing = dirs.data;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2 text-sm">
        <button
          type="button"
          className={btn}
          disabled={!listing}
          onClick={() => {
            onPath(listing?.parent ?? "");
          }}
        >
          {listing?.path ? "Up" : "Roots"}
        </button>
        <span aria-label="Current folder" className="truncate font-mono text-xs">
          {listing?.path || "Drives and roots"}
        </span>
      </div>
      {dirs.isError && <p role="alert" className="text-sm text-danger">{describeError(dirs.error)}</p>}
      <ul aria-label="Folders" className="max-h-56 overflow-auto rounded-md border border-line">
        {listing?.entries.map((e) => (
          <li key={e.path}>
            <button
              type="button"
              className="flex w-full items-center justify-between px-2 py-1 text-left text-sm hover:bg-surface"
              onClick={() => {
                onPath(e.path);
              }}
            >
              <span className="truncate">{e.name}</span>
              {e.isRepo && <span className="ml-2 rounded-sm border border-line px-1 text-xs text-muted">git repo</span>}
            </button>
          </li>
        ))}
        {listing?.entries.length === 0 && <li className="px-2 py-1 text-sm text-muted">No subfolders.</li>}
      </ul>
      <div>
        <button type="button" className={btnPrimary} disabled={!listing?.path} onClick={() => { if (listing?.path) onChoose(listing.path); }}>
          Preview this folder
        </button>
      </div>
    </div>
  );
}

/** Repo checkboxes shared by the add and rescan flows. */
export function CandidateList({
  candidates,
  checked,
  onToggle,
}: {
  candidates: CandidateDTO[];
  checked: ReadonlySet<string>;
  onToggle: (relPath: string) => void;
}) {
  return (
    <ul aria-label="Repos found" className="max-h-56 overflow-auto rounded-md border border-line">
      {candidates.map((c) => (
        <li key={c.relPath} className="flex items-center gap-2 px-2 py-1 text-sm">
          <input
            id={`cand-${c.relPath}`}
            type="checkbox"
            disabled={c.active}
            checked={c.active || checked.has(c.relPath)}
            onChange={() => {
              onToggle(c.relPath);
            }}
          />
          <label htmlFor={`cand-${c.relPath}`} className="flex-1 truncate">
            {c.name} <span className="font-mono text-xs text-muted">{c.relPath}</span>
          </label>
          {c.active && <span className="text-xs text-muted">already tracked</span>}
        </li>
      ))}
    </ul>
  );
}

function Preview({ preview, onDone }: { preview: PreviewResponse; onDone: () => void }) {
  const [name, setName] = useState(lastSegment(preview.path));
  const { checked, toggle } = useChecked();
  const set = checked(preview.candidates);
  const track = useRepoMutation(trackProject);
  const picks = preview.candidates.filter((c) => !c.active && set.has(c.relPath)).map((c) => ({ relPath: c.relPath, name: c.name }));

  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        track.mutate({ path: preview.path, name: name.trim(), picks }, { onSuccess: onDone });
      }}
    >
      <p className="truncate font-mono text-xs">{preview.path}</p>
      {preview.existingProjectId && (
        <p className="text-sm text-muted">This folder is already a project. Tracking adds any repos you select.</p>
      )}
      {preview.candidates.length === 0 ? (
        <p className="text-sm text-muted">No git repos were found in this folder.</p>
      ) : (
        <CandidateList candidates={preview.candidates} checked={set} onToggle={toggle} />
      )}
      {preview.truncated && (
        <p className="text-sm text-muted">Showing the first {preview.candidates.length} repos found.</p>
      )}
      {preview.skipped > 0 && (
        <p className="text-sm text-muted">{preview.skipped} subfolders could not be read.</p>
      )}
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
      {track.isError && <p role="alert" className="text-sm text-danger">{describeError(track.error)}</p>}
      <div className="flex justify-end gap-2">
        <Dialog.Close className={btn}>Cancel</Dialog.Close>
        <button type="submit" className={btnPrimary} disabled={track.isPending || name.trim() === ""}>
          Track
        </button>
      </div>
    </form>
  );
}

export function AddProjectDialog({ trigger }: { trigger: React.ReactNode }) {
  const [open, setOpen] = useState(false);
  const [path, setPath] = useState("");
  const preview = useMutation({ mutationFn: previewProject });

  return (
    <Dialog.Root
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) {
          setPath("");
          preview.reset();
        }
      }}
    >
      <Dialog.Trigger asChild>{trigger}</Dialog.Trigger>
      <DialogShell title="Add project" description="Pick a folder; repos inside it are tracked. Nothing on disk is changed.">
        {preview.data ? (
          <Preview
            preview={preview.data}
            onDone={() => {
              setOpen(false);
              setPath("");
              preview.reset();
            }}
          />
        ) : (
          <>
            <Browser path={path} onPath={setPath} onChoose={(p) => { preview.mutate(p); }} />
            {preview.isError && <p role="alert" className="text-sm text-danger">{describeError(preview.error)}</p>}
          </>
        )}
      </DialogShell>
    </Dialog.Root>
  );
}
