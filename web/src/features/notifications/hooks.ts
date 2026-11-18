import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { TopicNotices } from "../../api/gen/api.gen";
import { useWS } from "../../api/wsContext";
import { fetchNoticePage, MAX_PAGE } from "./api";
import { mergeNotices, type NoticeView } from "./core";
import { readPermission, useDeliveryStore } from "./store";

export const noticeKeys = { all: ["notices"] as const };

const FIRST_PAGE = 50;
const MAX_PAGES = 5;

/**
 * Reads history. The first read is the newest notices; later reads ask only for notices after the
 * highest cursor held, page by page, and fold them into the cached view.
 */
async function loadNotices(qc: QueryClient, signal: AbortSignal): Promise<NoticeView> {
  const prev = qc.getQueryData<NoticeView>(noticeKeys.all);
  if (!prev) return mergeNotices(undefined, await fetchNoticePage(undefined, FIRST_PAGE, signal));
  let view = prev;
  let fresh: NoticeView["fresh"] = [];
  for (let i = 0; i < MAX_PAGES; i++) {
    const page = await fetchNoticePage(view.newest, MAX_PAGE, signal);
    const next = mergeNotices(view, page);
    if (next.kind === "gap") return next;
    if (next.kind === "live") fresh = [...fresh, ...next.fresh];
    view = next;
    if (page.notices.length < MAX_PAGE) break;
  }
  return fresh.length > 0 ? { ...view, fresh, kind: "live" } : { ...prev, fresh: [], kind: "unchanged" };
}

/** Notification history (UI-06). */
export function useNotices() {
  const qc = useQueryClient();
  return useQuery({ queryKey: noticeKeys.all, queryFn: ({ signal }) => loadNotices(qc, signal) });
}

/**
 * Mounted once in the app shell. Keeps history fresh from the "notices" topic and feeds the
 * browser-delivery machine: live notices as LiveNotice, first and gap loads as HistoryLoaded
 * (which never shows anything). Renders nothing and never asks for permission.
 */
export function useNoticeSync(): void {
  const ws = useWS();
  const qc = useQueryClient();
  const dispatch = useDeliveryStore((s) => s.dispatch);
  const { data } = useNotices();

  useEffect(() => {
    dispatch({ tag: "capabilityChecked", permission: readPermission() });
    const onVisible = () => {
      dispatch({ tag: "visibilityChanged", visible: document.visibilityState !== "hidden" });
    };
    onVisible();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [dispatch]);

  useEffect(() => {
    const refresh = () => {
      void qc.invalidateQueries({ queryKey: noticeKeys.all });
    };
    return ws.subscribe(TopicNotices, { onEvent: refresh, onGap: refresh });
  }, [ws, qc]);

  // The highest cursor already handed to the delivery machine. Reading it (not the `fresh` list)
  // keeps a live notice from being skipped when two reads land before one render.
  const handled = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (!data) return;
    const was = handled.current;
    if (data.kind === "initial" || data.kind === "gap" || was === undefined) {
      handled.current = data.newest;
      dispatch({ tag: "historyLoaded", ids: data.notices.map((n) => n.id) });
      return;
    }
    const added = data.notices.filter((n) => n.cursor > was).sort((a, b) => a.cursor - b.cursor);
    handled.current = Math.max(was, data.newest);
    for (const n of added) dispatch({ tag: "liveNotice", id: n.id, title: n.taskTitle, status: n.status });
  }, [data, dispatch]);
}
