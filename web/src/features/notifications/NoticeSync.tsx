import { useNoticeSync } from "./hooks";

/** Mounted once in the app shell: keeps notice history live and drives browser delivery. Renders nothing. */
export function NoticeSync() {
  useNoticeSync();
  return null;
}
