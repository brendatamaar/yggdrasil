import { useState } from "react";
import type { ReviewNoteDTO, WorktreeDiffFileDTO } from "../../api/gen/api.gen";
import { describeError } from "../../api/client";
import { isNotReady, type DiffLoader } from "./api";
import {
  closedEditor,
  diffHash,
  parseFileDiffs,
  sameAnchor,
  stepNote,
  type Anchor,
  type DiffRow,
  type FileDiff,
  type NoteEditor,
  type NoteInput,
  type SaveCommand,
} from "./core";
import { useRepoDiff, useReviewMutations } from "./hooks";

const rowClass = { add: "bg-ok/15", del: "bg-danger/15", ctx: "" } as const;
const mark = { add: "+", del: "-", ctx: " " } as const;

function anchorOf(repoId: string, path: string, r: DiffRow): Anchor {
  return r.kind === "del" ? { repoId, path, line: r.oldNo ?? 0, side: "old" } : { repoId, path, line: r.newNo ?? 0, side: "new" };
}

function NoteView({ note, onEdit, onDelete }: { note: ReviewNoteDTO; onEdit: () => void; onDelete: () => void }) {
  const outdated = note.state === "outdated";
  return (
    <div
      data-note-state={note.state}
      className={`my-1 rounded border p-2 font-sans text-sm ${outdated ? "border-dashed border-warn opacity-80" : "border-line"}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        {outdated ? <span className="rounded-full border border-warn px-2 text-xs text-warn">Outdated: the file changed since</span> : null}
        {note.state === "sent" ? <span className="text-xs text-muted">Sent</span> : null}
        {note.state === "sending" ? <span className="text-xs text-muted">Sending…</span> : null}
        {/* Plain text only: never rendered as markup. */}
        <span className="whitespace-pre-wrap">{note.text}</span>
      </div>
      {note.state === "open" || outdated ? (
        <div className="mt-1 flex gap-2 text-xs">
          <button type="button" className="underline" onClick={onEdit}>
            Edit note
          </button>
          <button type="button" className="underline" onClick={onDelete}>
            Delete note
          </button>
        </div>
      ) : null}
    </div>
  );
}

function Editor({ state, dispatch }: { state: Exclude<NoteEditor, { kind: "closed" }>; dispatch: (i: NoteInput) => void }) {
  const saving = state.kind === "saving";
  return (
    <form
      className="my-1 space-y-1 font-sans"
      onSubmit={(e) => {
        e.preventDefault();
        dispatch({ type: "submit" });
      }}
    >
      <textarea
        autoFocus
        aria-label="Note"
        className="w-full rounded border border-line bg-bg p-1 text-sm"
        value={state.text}
        disabled={saving}
        onChange={(e) => {
          dispatch({ type: "type", text: e.target.value });
        }}
        onKeyDown={(e) => {
          if (e.key === "Escape") dispatch({ type: "cancel" });
          if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) dispatch({ type: "submit" });
        }}
      />
      {state.kind === "editing" && state.error ? (
        <p role="alert" className="text-sm text-danger">
          {state.error}
        </p>
      ) : null}
      <div className="flex gap-2 text-sm">
        <button type="submit" disabled={saving || state.text.trim() === ""} className="rounded border border-line px-2 py-0.5 hover:bg-surface">
          Save note
        </button>
        <button
          type="button"
          disabled={saving}
          className="rounded border border-line px-2 py-0.5 hover:bg-surface"
          onClick={() => {
            dispatch({ type: "cancel" });
          }}
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

function FileView({
  repoId,
  file,
  notes,
  editor,
  dispatch,
  onDelete,
  anchorable,
}: {
  repoId: string;
  anchorable: boolean;
  file: FileDiff;
  notes: ReviewNoteDTO[];
  editor: NoteEditor;
  dispatch: (i: NoteInput) => void;
  onDelete: (id: string) => void;
}) {
  const visible = file.hunks.filter((h) => !h.eolOnly);
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2 text-sm">
        <span className="font-mono">{file.path}</span>
        {file.eolChanged ? (
          <span className="rounded-full border border-line px-2 text-xs text-muted" title="Some changes in this file only switch line endings and are hidden.">
            line endings changed
          </span>
        ) : null}
      </div>
      {anchorable ? null : (
        <p className="text-xs text-muted">Notes can't be added here: the patch for this file is cut short.</p>
      )}
      {visible.length === 0 ? (
        <p className="text-sm text-muted">Only line endings changed; there are no other changes to show.</p>
      ) : (
        <div role="region" aria-label={`Diff of ${file.path}`} tabIndex={0} className="overflow-x-auto rounded-md border border-line font-mono text-xs">
          <table className="w-full border-collapse">
            <tbody>
              {visible.map((h, hi) => [
                <tr key={`h${String(hi)}`} className="bg-surface text-accent">
                  <td colSpan={4} className="px-2">
                    {h.header}
                  </td>
                </tr>,
                ...h.rows.flatMap((r, ri) => {
                  const a = anchorOf(repoId, file.path, r);
                  const here = notes.filter((n) => sameAnchor(n, a));
                  const editing = editor.kind !== "closed" && sameAnchor(a, editor.anchor);
                  return [
                    <tr key={`r${String(hi)}-${String(ri)}`} className={rowClass[r.kind]}>
                      <td className="w-6 px-1">
                        <button
                          type="button"
                          disabled={!anchorable}
                          aria-label={`Add note on line ${String(a.line)} of ${file.path}`}
                          className="rounded px-1 text-muted hover:bg-surface hover:text-fg focus:bg-surface"
                          onClick={() => {
                            dispatch({ type: "open", anchor: a, text: "" });
                          }}
                        >
                          +
                        </button>
                      </td>
                      <td aria-hidden className="select-none px-2 text-right text-muted">
                        {r.kind === "del" ? r.oldNo : r.newNo}
                      </td>
                      <td className="whitespace-pre pr-3" colSpan={2}>
                        <span aria-hidden className="select-none">{mark[r.kind]}</span>
                        {r.text.replace(/\r$/, "")}
                      </td>
                    </tr>,
                    ...(here.length > 0 || editing
                      ? [
                          <tr key={`n${String(hi)}-${String(ri)}`}>
                            <td colSpan={4} className="px-2">
                              {here.map((n) => (
                                <NoteView
                                  key={n.id}
                                  note={n}
                                  onEdit={() => {
                                    dispatch({ type: "open", anchor: a, noteId: n.id, text: n.text });
                                  }}
                                  onDelete={() => {
                                    onDelete(n.id);
                                  }}
                                />
                              ))}
                              {editing ? <Editor state={editor} dispatch={dispatch} /> : null}
                            </td>
                          </tr>,
                        ]
                      : []),
                  ];
                }),
              ])}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

/** The worktree diff of one repo with inline notes. */
export function RepoDiff({
  taskId,
  repoId,
  repoName,
  notes,
  load,
}: {
  taskId: string;
  repoId: string;
  repoName: string;
  notes: ReviewNoteDTO[];
  load: DiffLoader;
}) {
  const diff = useRepoDiff(taskId, repoId, load);
  const m = useReviewMutations(taskId);
  const [editor, setEditor] = useState<NoteEditor>(closedEditor);
  const files = parseFileDiffs(diff.data?.text ?? "");
  const meta = new Map<string, WorktreeDiffFileDTO>((diff.data?.files ?? []).map((f) => [f.path, f]));
  const anchorableFile = (f: FileDiff) => {
    const m = meta.get(f.path);
    return !m || !(m.binary || m.patchTruncated || m.omitted);
  };
  const listed = new Set(files.map((f) => f.path));
  const unshown = (diff.data?.files ?? []).filter((f) => !listed.has(f.path));

  const run = (cmd: SaveCommand) => {
    const file = files.find((f) => f.path === cmd.anchor.path);
    const serverHash = meta.get(cmd.anchor.path)?.hash;
    void (async () => {
      try {
        const create = file
          ? { repoId, path: cmd.anchor.path, line: cmd.anchor.line, side: cmd.anchor.side, text: cmd.text, diffHash: serverHash || (await diffHash(file.text)) }
          : undefined;
        await m.saveNote.mutateAsync({ noteId: cmd.noteId, text: cmd.text, create });
        setEditor((s) => stepNote(s, { type: "saved" })[0]);
      } catch (e) {
        setEditor((s) => stepNote(s, { type: "failed", message: describeError(e) })[0]);
      }
    })();
  };
  const dispatch = (input: NoteInput) => {
    const [next, cmds] = stepNote(editor, input);
    setEditor(next);
    cmds.forEach(run);
  };

  return (
    <section aria-label={`Changes in ${repoName}`} className="space-y-2">
      <h3 className="font-medium">{repoName}</h3>
      {diff.isPending ? <p className="text-sm text-muted">Loading the changes…</p> : null}
      {diff.error ? (
        isNotReady(diff.error) ? (
          <p role="status" className="text-sm text-muted">Changes for this repo aren't available yet.</p>
        ) : (
          <p role="alert" className="text-sm text-danger">{describeError(diff.error)}</p>
        )
      ) : null}
      {diff.data?.truncated ? <p className="text-sm text-warn">This diff is too large; only the start is shown.</p> : null}
      {diff.data?.binary ? <p className="text-sm text-warn">Binary files can't be shown as text.</p> : null}
      {diff.data && files.length === 0 && !diff.data.binary ? <p className="text-sm text-muted">No changes to show.</p> : null}
      {unshown.length > 0 ? (
        <ul aria-label="Files without a text diff" className="text-sm text-muted">
          {unshown.map((f) => (
            <li key={f.path}>
              <span className="font-mono">{f.path}</span>: {f.binary ? "binary file" : "too large to show"}. Notes can't be added on it.
            </li>
          ))}
        </ul>
      ) : null}
      {files.map((f) => (
        <FileView
          key={f.path}
          anchorable={anchorableFile(f)}
          repoId={repoId}
          file={f}
          notes={notes.filter((n) => n.path === f.path)}
          editor={editor}
          dispatch={dispatch}
          onDelete={(id) => {
            m.deleteNote.mutate(id);
          }}
        />
      ))}
    </section>
  );
}
