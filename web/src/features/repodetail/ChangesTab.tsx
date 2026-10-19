import { useState } from "react";
import { describeError } from "../../api/client";
import type { FileChangeDTO } from "../../api/gen/api.gen";
import type { DiffMode } from "../../api/repodetail";
import { DiffView } from "./DiffView";
import { useChanges, useDiff } from "./hooks";
import { NavList, Notice } from "./parts";
import { itemBtn } from "./styles";

const code = (f: FileChangeDTO) => (f.index === "?" || f.worktree === "?" ? "??" : `${f.index || " "}${f.worktree || " "}`);

export function FileDiff({ repoId, path, mode }: { repoId: string; path: string; mode: DiffMode }) {
  const q = useDiff(repoId, path, mode);
  if (q.isPending) return <Notice>Loading diff…</Notice>;
  if (q.isError) return <Notice tone="danger">{describeError(q.error)}</Notice>;
  return <DiffView diff={q.data} label={`Diff of ${path}`} />;
}

export function ChangesTab({ repoId }: { repoId: string }) {
  const q = useChanges(repoId);
  const [selected, setSelected] = useState<string | null>(null);
  const [override, setOverride] = useState<DiffMode | null>(null);
  if (q.isPending) return <Notice>Loading changes…</Notice>;
  if (q.isError) return <Notice tone="danger">{describeError(q.error)}</Notice>;
  const files = q.data.files;
  if (files.length === 0) return <Notice>No uncommitted changes.</Notice>;

  const file = files.find((f) => f.path === selected);
  const defaultMode: DiffMode = file && !file.worktree.trim() && file.index.trim() ? "staged" : "unstaged";
  const mode = override ?? defaultMode;

  return (
    <div className="grid gap-3 md:grid-cols-[minmax(12rem,20rem)_1fr]">
      <div>
        <NavList label="Changed files" className="divide-y divide-line rounded-md border border-line">
          {files.map((f) => (
            <li key={f.path}>
              <button
                type="button"
                data-nav
                aria-current={f.path === selected}
                onClick={() => {
                  setSelected(f.path);
                  setOverride(null);
                }}
                className={itemBtn(f.path === selected)}
              >
                <span aria-label={`status ${code(f).trim()}`} className="w-6 shrink-0 whitespace-pre font-mono text-xs text-warn">{code(f)}</span>
                <span className="min-w-0 truncate" title={f.path}>{f.path}</span>
              </button>
            </li>
          ))}
        </NavList>
        {q.data.truncated && <Notice tone="warn">The list is cut short. Counts in the header are exact.</Notice>}
      </div>
      <div className="min-w-0 space-y-2">
        {file ? (
          <>
            <div className="flex items-center gap-2 text-sm">
              <span className="truncate font-mono" title={file.path}>{file.path}</span>
              <div role="group" aria-label="Diff side" className="ml-auto flex overflow-hidden rounded-md border border-line">
                {(["unstaged", "staged"] as const).map((m) => (
                  <button
                    key={m}
                    type="button"
                    aria-pressed={mode === m}
                    onClick={() => {
                      setOverride(m);
                    }}
                    className={`px-2 py-0.5 ${mode === m ? "bg-surface font-medium" : "hover:bg-surface"}`}
                  >
                    {m === "staged" ? "Staged" : "Unstaged"}
                  </button>
                ))}
              </div>
            </div>
            <FileDiff repoId={repoId} path={file.path} mode={mode} />
          </>
        ) : (
          <Notice>Select a file to see its diff.</Notice>
        )}
      </div>
    </div>
  );
}
