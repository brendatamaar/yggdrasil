import { needsYouText } from "./core";

/** Why a task is in "Needs you", driven by the board card's `needsYouReason`. Nothing for an unknown reason. */
export function NeedsYouNotice({ reason }: { reason: string | undefined }) {
  const text = needsYouText(reason);
  if (!text) return null;
  const serious = reason === "integrity_violated";
  return (
    <div
      role={serious ? "alert" : "status"}
      className={`rounded border-2 p-3 text-sm ${serious ? "border-danger" : "border-line"}`}
      data-reason={reason}
    >
      <p className="font-medium">{text.title}</p>
      <p className="text-muted">{text.body}</p>
    </div>
  );
}
