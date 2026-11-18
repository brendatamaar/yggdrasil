// Status wording for tasks. Notification history is server data (TanStack Query) and lives in
// features/notifications; this file keeps only the label table other screens use.

const statusLabels: Record<string, string> = {
  needs_you: "Needs you",
  review: "Ready for review",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
  running: "Running",
  queued: "Queued",
  draft: "Draft",
};

export function statusLabel(status: string): string {
  return statusLabels[status] ?? status;
}
