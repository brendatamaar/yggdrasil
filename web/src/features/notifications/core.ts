// Pure decisions of notification history and browser delivery (docs/design/phase-6-notifications.md,
// docs/AGENTS.md 5.9 and 5.11). No IO, no clock. A notice's visible text is the task name and a
// status label, nothing else.
import type { NoticeDTO, NoticesResponse } from "../../api/gen/api.gen";

// --- Display text -----------------------------------------------------------

const STATUS_LABEL: Record<string, string> = {
  needs_you: "Needs you",
  verification_passed: "Checks passed",
  verification_failed: "Checks failed",
  ready_for_review: "Ready for review",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
};

export const noticeStatusLabel = (status: string): string => STATUS_LABEL[status] ?? status;

/** The whole text of a notice: the task name and the status. Anything else on the DTO is metadata. */
export function noticeText(n: Pick<NoticeDTO, "taskTitle" | "status">): { title: string; body: string } {
  return { title: n.taskTitle, body: noticeStatusLabel(n.status) };
}

// --- History (the shape kept in the TanStack Query cache) --------------------

export const KEEP_NOTICES = 200;

export type HistoryKind = "initial" | "live" | "gap" | "unchanged";

export interface NoticeView {
  /** Newest first. */
  notices: NoticeDTO[];
  /** The highest cursor held; the next read asks for notices after it. */
  newest: number;
  /** Notices that arrived after the previous read (oldest first); only set when kind is "live". */
  fresh: NoticeDTO[];
  kind: HistoryKind;
}

const byCursorDesc = (a: NoticeDTO, b: NoticeDTO) => b.cursor - a.cursor;

/**
 * Folds one server page into the history. The first page and a page with `gap` set are history
 * loads: they never produce `fresh` notices, so they can never cause a browser toast.
 */
export function mergeNotices(prev: NoticeView | undefined, resp: NoticesResponse): NoticeView {
  if (!prev) {
    return { notices: [...resp.notices].sort(byCursorDesc).slice(0, KEEP_NOTICES), newest: resp.newest, fresh: [], kind: "initial" };
  }
  if (resp.gap) {
    return { notices: [...resp.notices].sort(byCursorDesc).slice(0, KEEP_NOTICES), newest: resp.newest, fresh: [], kind: "gap" };
  }
  const have = new Set(prev.notices.map((n) => n.id));
  const added = resp.notices.filter((n) => n.cursor > prev.newest && !have.has(n.id));
  if (added.length === 0) return { ...prev, fresh: [], kind: "unchanged" };
  const notices = [...added, ...prev.notices].sort(byCursorDesc).slice(0, KEEP_NOTICES);
  const newest = Math.max(prev.newest, resp.newest, ...added.map((n) => n.cursor));
  return { notices, newest, fresh: [...added].sort((a, b) => a.cursor - b.cursor), kind: "live" };
}

/** Unread notices: those whose IDs the person has not marked read (UI-only IDs). */
export const unreadIds = (notices: readonly NoticeDTO[], read: ReadonlySet<string>): string[] =>
  notices.filter((n) => !read.has(n.id)).map((n) => n.id);

// --- Browser delivery --------------------------------------------------------

export type Permission = "unsupported" | "default" | "denied" | "granted";

interface Common {
  /** Whether the tab is in front. */
  visible: boolean;
  /** Notice IDs already handled this session (shown, or loaded as history). Never server data. */
  delivered: ReadonlySet<string>;
}
export type DeliveryState =
  | ({ tag: "unsupported" } & Common)
  | ({ tag: "default" } & Common)
  | ({ tag: "denied" } & Common)
  | ({ tag: "granted" } & Common);

export type DeliveryInput =
  | { tag: "capabilityChecked"; permission: Permission }
  | { tag: "permissionGesture" }
  | { tag: "permissionResolved"; permission: Exclude<Permission, "unsupported"> }
  | { tag: "visibilityChanged"; visible: boolean }
  | { tag: "liveNotice"; id: string; title: string; status: string }
  | { tag: "historyLoaded"; ids: readonly string[] }
  | { tag: "deliveryFailed"; id: string }
  | { tag: "closed" };

export type DeliveryCommand =
  | { tag: "requestPermission" }
  | { tag: "showBrowserNotice"; id: string; title: string; body: string };

export const initialDelivery = (): DeliveryState => ({ tag: "unsupported", visible: true, delivered: new Set() });

const MAX_DELIVERED = 1000;

const withTag = (s: DeliveryState, tag: Permission): DeliveryState => ({ tag, visible: s.visible, delivered: s.delivered });
const remember = (s: DeliveryState, ids: readonly string[]): DeliveryState => {
  const delivered = new Set(s.delivered);
  for (const id of ids) delivered.add(id);
  // Bounded: drop the oldest entries (a Set keeps insertion order).
  while (delivered.size > MAX_DELIVERED) delivered.delete(delivered.values().next().value as string);
  return { ...s, delivered };
};

export function stepDelivery(state: DeliveryState, input: DeliveryInput): [DeliveryState, DeliveryCommand[]] {
  switch (input.tag) {
    case "capabilityChecked":
      return [withTag(state, input.permission), []];
    case "permissionGesture":
      // The only way to a permission prompt: a person's click, and only while the answer is still open.
      return state.tag === "default" ? [state, [{ tag: "requestPermission" }]] : [state, []];
    case "permissionResolved":
      return state.tag === "unsupported" ? [state, []] : [withTag(state, input.permission), []];
    case "visibilityChanged":
      return [{ ...state, visible: input.visible }, []];
    case "liveNotice": {
      if (state.delivered.has(input.id)) return [state, []];
      const next = remember(state, [input.id]);
      if (state.tag !== "granted" || state.visible) return [next, []];
      const { title, body } = noticeText({ taskTitle: input.title, status: input.status });
      return [next, [{ tag: "showBrowserNotice", id: input.id, title, body }]];
    }
    case "historyLoaded":
      // Initial and gap loads fill the in-app list only: never a toast, now or later.
      return [remember(state, input.ids), []];
    case "deliveryFailed":
      // The browser refused one toast. The in-app notice is query data and is unaffected.
      return [state, []];
    case "closed":
      return [{ ...state, delivered: new Set() }, []];
  }
}
