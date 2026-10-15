import * as Popover from "@radix-ui/react-popover";
import { statusLabel, useNotificationStore } from "../stores/notifications";

export function NotificationsMenu() {
  const items = useNotificationStore((s) => s.items);
  const markAllRead = useNotificationStore((s) => s.markAllRead);
  const clear = useNotificationStore((s) => s.clear);
  const unread = items.filter((i) => !i.read).length;

  return (
    <Popover.Root
      onOpenChange={(open) => {
        if (open) markAllRead();
      }}
    >
      <Popover.Trigger asChild>
        <button
          type="button"
          aria-label={unread > 0 ? `Notifications, ${String(unread)} unread` : "Notifications"}
          className="rounded-md border border-line px-3 py-1 text-sm hover:bg-surface"
        >
          Notifications{unread > 0 ? ` (${String(unread)})` : ""}
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="end"
          sideOffset={6}
          className="w-72 rounded-md border border-line bg-surface p-2 text-sm shadow-lg"
        >
          {items.length === 0 ? (
            <p className="p-2 text-muted">No notifications.</p>
          ) : (
            <>
              <ul className="max-h-80 space-y-1 overflow-auto">
                {items.map((i) => (
                  <li key={i.id} className="rounded px-2 py-1">
                    <span className="font-medium">{i.taskName}</span>
                    <span className="text-muted">: {statusLabel(i.status)}</span>
                  </li>
                ))}
              </ul>
              <button type="button" onClick={clear} className="mt-2 rounded px-2 py-1 text-muted hover:bg-canvas">
                Clear all
              </button>
            </>
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
