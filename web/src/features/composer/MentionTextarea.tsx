import * as Popover from "@radix-ui/react-popover";
import { useId, useRef, useState, type KeyboardEvent } from "react";
import { findMention, insertMention, moveActive } from "./core";
import { useMentions } from "./hooks";

/**
 * Prompt textarea with the @ file picker: an ARIA combobox (textarea +
 * listbox, aria-activedescendant). Results are rendered as plain text.
 */
export function MentionTextarea({
  id,
  value,
  onChange,
  projectId,
  className,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  projectId: string;
  className?: string;
}) {
  const listId = useId();
  const ref = useRef<HTMLTextAreaElement>(null);
  const [caret, setCaret] = useState(0);
  const [dismissedAt, setDismissedAt] = useState<number | null>(null);
  const [active, setActive] = useState(0);

  const trigger = findMention(value, caret);
  const open = trigger !== null && dismissedAt !== trigger.start;
  const q = useMentions(projectId, trigger?.query ?? "", open);
  const items = q.data?.mentions ?? [];
  const activeIndex = Math.min(active, Math.max(items.length - 1, 0));
  const optionId = (i: number) => `${listId}-opt-${String(i)}`;

  const pick = (i: number) => {
    const m = items[i];
    if (!m || !trigger) return;
    const next = insertMention(value, caret, trigger, m.repoName, m.path);
    onChange(next.text);
    setCaret(next.caret);
    setDismissedAt(null);
    requestAnimationFrame(() => {
      ref.current?.focus();
      ref.current?.setSelectionRange(next.caret, next.caret);
    });
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
      e.preventDefault();
      e.currentTarget.form?.requestSubmit();
      return;
    }
    if (!open) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setActive(moveActive(activeIndex, items.length, e.key === "ArrowDown" ? "down" : "up"));
    } else if ((e.key === "Enter" || e.key === "Tab") && items.length > 0) {
      e.preventDefault();
      pick(activeIndex);
    } else if (e.key === "Escape") {
      e.preventDefault();
      setDismissedAt(trigger.start);
    }
  };

  return (
    <Popover.Root open={open}>
      <Popover.Anchor asChild>
        <textarea
          ref={ref}
          id={id}
          className={className}
          value={value}
          role="combobox"
          aria-expanded={open}
          aria-controls={open ? listId : undefined}
          aria-autocomplete="list"
          aria-activedescendant={open && items.length > 0 ? optionId(activeIndex) : undefined}
          onChange={(e) => {
            onChange(e.target.value);
            setCaret(e.target.selectionStart);
            setActive(0);
          }}
          onSelect={(e) => {
            setCaret(e.currentTarget.selectionStart);
          }}
          onKeyDown={onKeyDown}
        />
      </Popover.Anchor>
      <Popover.Portal>
        <Popover.Content
          align="start"
          sideOffset={4}
          onOpenAutoFocus={(e) => {
            e.preventDefault();
          }}
          onInteractOutside={(e) => {
            if (e.target === ref.current) e.preventDefault();
          }}
          className="z-50 max-h-64 w-80 overflow-auto rounded border border-line bg-canvas p-1 text-sm shadow-lg"
        >
          <ul id={listId} role="listbox" aria-label="Files and folders">
            {q.isPending ? <li className="px-2 py-1 text-muted">Searching...</li> : null}
            {q.isError ? <li className="px-2 py-1 text-danger">Could not load files. Keep typing to retry.</li> : null}
            {q.isSuccess && items.length === 0 ? <li className="px-2 py-1 text-muted">No matching files.</li> : null}
            {items.map((m, i) => (
              <li
                key={`${m.repoId}:${m.path}`}
                id={optionId(i)}
                role="option"
                aria-selected={i === activeIndex}
                className={`cursor-pointer rounded px-2 py-1 ${i === activeIndex ? "bg-accent/20" : ""}`}
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(i);
                }}
              >
                <span className="text-muted">{m.kind === "dir" ? "folder " : "file "}</span>
                {m.repoName}/{m.path}
              </li>
            ))}
            {q.data?.truncated ? <li className="px-2 py-1 text-xs text-muted">More results; keep typing.</li> : null}
          </ul>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
