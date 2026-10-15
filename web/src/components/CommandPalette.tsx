import { Command } from "cmdk";
import { useEffect, useMemo } from "react";
import { useCommandStore } from "../stores/commands";

export function CommandPalette() {
  const open = useCommandStore((s) => s.open);
  const setOpen = useCommandStore((s) => s.setOpen);
  const commands = useCommandStore((s) => s.commands);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen(!useCommandStore.getState().open);
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [setOpen]);

  const groups = useMemo(() => {
    const by = new Map<string, (typeof commands)[string][]>();
    for (const c of Object.values(commands)) {
      const g = c.group ?? "Commands";
      by.set(g, [...(by.get(g) ?? []), c]);
    }
    return [...by.entries()];
  }, [commands]);

  return (
    <Command.Dialog
      open={open}
      onOpenChange={setOpen}
      label="Command palette"
      overlayClassName="fixed inset-0 bg-black/40"
      contentClassName="fixed left-1/2 top-24 w-[32rem] max-w-[90vw] -translate-x-1/2 rounded-lg border border-line bg-surface p-2 shadow-xl"
    >
      <Command.Input
        placeholder="Type a command"
        className="w-full rounded-md border border-line bg-canvas px-3 py-2 text-sm"
      />
      <Command.List className="mt-2 max-h-80 overflow-auto">
        <Command.Empty className="p-2 text-sm text-muted">No matching command.</Command.Empty>
        {groups.map(([group, items]) => (
          <Command.Group key={group} heading={group} className="text-xs text-muted">
            {items.map((c) => (
              <Command.Item
                key={c.id}
                value={c.title}
                keywords={c.keywords}
                onSelect={() => {
                  setOpen(false);
                  c.run();
                }}
                className="cursor-pointer rounded px-2 py-1.5 text-sm text-fg data-[selected=true]:bg-canvas"
              >
                {c.title}
              </Command.Item>
            ))}
          </Command.Group>
        ))}
      </Command.List>
    </Command.Dialog>
  );
}
