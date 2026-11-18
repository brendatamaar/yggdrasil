import { apiFetch } from "../../api/client";
import type { NoticeDTO, NoticesResponse } from "../../api/gen/api.gen";

export const MAX_PAGE = 200;

const STATUSES = new Set(["needs_you", "verification_passed", "verification_failed", "ready_for_review", "done", "failed", "cancelled"]);

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null;

/** Accepts only a well-formed notice; the fields that become text are strings, the status a known value. */
export function parseNotice(raw: unknown): NoticeDTO | null {
  if (!isObj(raw)) return null;
  const { id, taskId, taskTitle, status, revision, createdAt, cursor } = raw;
  if (typeof id !== "string" || typeof taskId !== "string" || typeof taskTitle !== "string") return null;
  if (typeof status !== "string" || !STATUSES.has(status)) return null;
  if (typeof revision !== "number" || typeof createdAt !== "string" || typeof cursor !== "number") return null;
  return { id, taskId, taskTitle, status, revision, createdAt, cursor };
}

export class NoticeDecodeError extends Error {
  constructor() {
    super("Yggdrasil sent notifications this page does not understand. Reload the page.");
    this.name = "NoticeDecodeError";
  }
}

export function parseNotices(raw: unknown): NoticesResponse {
  if (!isObj(raw) || !Array.isArray(raw.notices) || typeof raw.newest !== "number" || typeof raw.oldest !== "number" || typeof raw.gap !== "boolean") {
    throw new NoticeDecodeError();
  }
  const notices = raw.notices.map(parseNotice);
  if (notices.some((n) => n === null)) throw new NoticeDecodeError();
  return { notices: notices as NoticeDTO[], oldest: raw.oldest, newest: raw.newest, gap: raw.gap };
}

/** One page: no cursor is the newest notices; `after` is the notices after it. */
export async function fetchNoticePage(after: number | undefined, limit: number, signal?: AbortSignal): Promise<NoticesResponse> {
  const q = new URLSearchParams({ limit: String(Math.min(limit, MAX_PAGE)) });
  if (after !== undefined) q.set("after", String(after));
  return parseNotices(await apiFetch<unknown>(`/api/notifications?${q.toString()}`, signal ? { signal } : undefined));
}
