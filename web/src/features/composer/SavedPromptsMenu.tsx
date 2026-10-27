import * as Popover from "@radix-ui/react-popover";
import { useState } from "react";
import { describeError } from "../../api/client";
import type { SavedPromptDTO } from "../../api/gen/api.gen";
import { usePromptMutations, useSavedPrompts } from "./hooks";

type Row = { tag: "none" } | { tag: "rename"; id: string; name: string } | { tag: "delete"; id: string };

const btn = "rounded border border-line px-2 py-0.5 text-xs";

/** Insert, save, rename and delete saved prompts. Names and texts are rendered as plain text. */
export function SavedPromptsMenu({ text, onInsert }: { text: string; onInsert: (text: string) => void }) {
  const [open, setOpen] = useState(false);
  const [row, setRow] = useState<Row>({ tag: "none" });
  const [newName, setNewName] = useState("");
  const prompts = useSavedPrompts();
  const m = usePromptMutations();
  const error = m.save.error ?? m.remove.error ?? m.rename.error;

  const save = () => {
    const name = newName.trim();
    if (name === "" || text.trim() === "") return;
    m.save.mutate({ name, text }, { onSuccess: () => { setNewName(""); } });
  };

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className={btn}>Saved prompts</Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="start"
          sideOffset={4}
          aria-label="Saved prompts"
          className="z-50 w-80 space-y-2 rounded border border-line bg-canvas p-2 text-sm shadow-lg"
        >
          {prompts.isPending ? <p className="text-muted">Loading...</p> : null}
          {prompts.isError ? <p role="alert" className="text-danger">{describeError(prompts.error)}</p> : null}
          {prompts.isSuccess && prompts.data.length === 0 ? <p className="text-muted">No saved prompts yet.</p> : null}
          <ul className="space-y-1">
            {(prompts.data ?? []).map((p: SavedPromptDTO) => (
              <li key={p.id} className="flex flex-wrap items-center gap-1">
                {row.tag === "rename" && row.id === p.id ? (
                  <form
                    className="flex w-full gap-1"
                    onSubmit={(e) => {
                      e.preventDefault();
                      e.stopPropagation(); // React submit events bubble through the portal to the task form
                      if (row.name.trim() === "") return;
                      m.rename.mutate({ id: p.id, name: row.name.trim(), text: p.text }, { onSuccess: () => { setRow({ tag: "none" }); } });
                    }}
                  >
                    <input
                      aria-label={`New name for ${p.name}`}
                      className="min-w-0 flex-1 rounded border border-line bg-canvas px-1"
                      value={row.name}
                      maxLength={80}
                      autoFocus
                      onChange={(e) => { setRow({ tag: "rename", id: p.id, name: e.target.value }); }}
                      onKeyDown={(e) => { if (e.key === "Escape") { e.stopPropagation(); setRow({ tag: "none" }); } }}
                    />
                    <button type="submit" className={btn}>Save name</button>
                  </form>
                ) : row.tag === "delete" && row.id === p.id ? (
                  <>
                    <span className="flex-1">Delete saved prompt "{p.name}"?</span>
                    <button
                      type="button"
                      className={`${btn} text-danger`}
                      onClick={() => { m.remove.mutate(p.id, { onSuccess: () => { setRow({ tag: "none" }); } }); }}
                    >
                      Delete
                    </button>
                    <button type="button" className={btn} autoFocus onClick={() => { setRow({ tag: "none" }); }}>
                      Keep
                    </button>
                  </>
                ) : (
                  <>
                    <button
                      type="button"
                      className="min-w-0 flex-1 truncate rounded px-1 text-left hover:bg-accent/20"
                      title={p.text}
                      onClick={() => { onInsert(p.text); setOpen(false); }}
                    >
                      {p.name}
                    </button>
                    <button type="button" className={btn} aria-label={`Rename ${p.name}`} onClick={() => { setRow({ tag: "rename", id: p.id, name: p.name }); }}>
                      Rename
                    </button>
                    <button type="button" className={btn} aria-label={`Delete ${p.name}`} onClick={() => { setRow({ tag: "delete", id: p.id }); }}>
                      Delete
                    </button>
                  </>
                )}
              </li>
            ))}
          </ul>
          <form
            className="flex gap-1 border-t border-line pt-2"
            onSubmit={(e) => { e.preventDefault(); e.stopPropagation(); save(); }}
          >
            <input
              aria-label="Name for the current text"
              placeholder="Save current text as..."
              className="min-w-0 flex-1 rounded border border-line bg-canvas px-1"
              maxLength={80}
              value={newName}
              onChange={(e) => { setNewName(e.target.value); }}
            />
            <button type="submit" className={btn} disabled={newName.trim() === "" || text.trim() === ""}>
              Save
            </button>
          </form>
          {error ? <p role="alert" className="text-danger">{describeError(error)}</p> : null}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
