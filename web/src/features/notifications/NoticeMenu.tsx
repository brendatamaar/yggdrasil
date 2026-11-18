import * as Popover from "@radix-ui/react-popover";
import { describeError } from "../../api/client";
import { NoticeDecodeError } from "./api";
import { noticeStatusLabel, unreadIds } from "./core";
import { useNotices } from "./hooks";
import { useDeliveryStore, useNoticeReadStore } from "./store";

const btn = "rounded border border-line px-2 py-1 text-sm hover:bg-canvas";

function DesktopNote() {
  const tag = useDeliveryStore((s) => s.state.tag);
  const dispatch = useDeliveryStore((s) => s.dispatch);
  switch (tag) {
    case "default":
      return (
        <div className="space-y-1 border-b border-line p-2">
          <p className="text-muted">Get a desktop notice when a task needs you while this tab is in the background.</p>
          <button
            type="button"
            className={btn}
            onClick={() => {
              dispatch({ tag: "permissionGesture" });
            }}
          >
            Turn on desktop notices
          </button>
        </div>
      );
    case "denied":
      return <p className="border-b border-line p-2 text-muted">Desktop notices are blocked in your browser settings. Notices still show here.</p>;
    case "unsupported":
      return <p className="border-b border-line p-2 text-muted">This browser can't show desktop notices. Notices still show here.</p>;
    case "granted":
      return null;
  }
}

/** The in-app notice list (UI-06): history from the server, unread as UI-only IDs. Text is the task name and status. */
export function NoticeMenu() {
  const q = useNotices();
  const read = useNoticeReadStore((s) => s.readIds);
  const markRead = useNoticeReadStore((s) => s.markRead);
  const notices = q.data?.notices ?? [];
  const unread = unreadIds(notices, new Set(read)).length;

  return (
    <Popover.Root
      onOpenChange={(open) => {
        if (open) markRead(notices.map((n) => n.id));
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
        <Popover.Content align="end" sideOffset={6} className="w-80 rounded-md border border-line bg-surface text-sm text-fg shadow-lg">
          <DesktopNote />
          <div className="p-2">
            {q.isPending ? (
              <p role="status" className="text-muted">
                Loading notifications…
              </p>
            ) : q.isError ? (
              <div className="space-y-1">
                <p role="alert" className="text-danger">
                  {q.error instanceof NoticeDecodeError ? q.error.message : `Couldn't load notifications. ${describeError(q.error)}`}
                </p>
                <button
                  type="button"
                  className={btn}
                  onClick={() => {
                    void q.refetch();
                  }}
                >
                  Try again
                </button>
              </div>
            ) : notices.length === 0 ? (
              <p data-empty="NoNotifications" className="text-muted">
                No notifications yet. Finished and waiting tasks show up here.
              </p>
            ) : (
              <ul aria-label="Notifications" className="max-h-80 space-y-1 overflow-auto">
                {notices.map((n) => (
                  <li key={n.id} className="rounded px-2 py-1">
                    <span className="font-medium">{n.taskTitle}</span>
                    <span className="text-muted">: {noticeStatusLabel(n.status)}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
