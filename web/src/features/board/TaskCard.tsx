import { Link } from "@tanstack/react-router";
import type { BoardCardDTO } from "../../api/gen/api.gen";
import { VerifyStateBadge } from "../../components/VerifyStateBadge";
import { canCancel, elapsedText, isWarnMode, modeLabel, statusText, usageText } from "./ui";

interface Props {
  card: BoardCardDTO;
  cancelling: boolean;
  onCancel: (id: string) => void;
}

export function TaskCard({ card, cancelling, onCancel }: Props) {
  const warn = isWarnMode(card.mode);
  return (
    <li className="rounded-md border border-line bg-surface p-3 text-sm">
      {/* Titles are plain text: React escapes them; never inject as HTML. */}
      <Link
        to="/tasks/$taskId"
        params={{ taskId: card.taskId }}
        data-card-link
        className="block break-words font-medium text-fg hover:underline"
      >
        {card.title}
      </Link>
      <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
        <span className="text-muted">{card.agent}</span>
        <span
          className={
            warn
              ? "rounded-sm border border-warn px-1.5 py-0.5 font-medium text-warn"
              : "rounded-sm border border-line px-1.5 py-0.5 text-muted"
          }
        >
          {warn ? `Warning: ${modeLabel(card.mode)}` : modeLabel(card.mode)}
        </span>
        {card.verification && card.verification.state !== "none" && (
          <span aria-label={`Verification: ${card.verification.state}`} className="inline-flex">
            <VerifyStateBadge state={card.verification.state} />
          </span>
        )}
        {card.pendingProposals > 0 && (
          <span className="rounded-sm border border-accent px-1.5 py-0.5 font-medium text-accent">
            {card.pendingProposals === 1 ? "1 proposal" : `${String(card.pendingProposals)} proposals`}
          </span>
        )}
      </div>
      {card.repoNames.length > 0 && <p className="mt-1 break-words text-xs text-muted">{card.repoNames.join(", ")}</p>}
      <p className="mt-1 text-xs">{statusText(card)}</p>
      <p className="mt-1 text-xs text-muted">
        {elapsedText(card)} · {usageText(card)}
      </p>
      <div className="mt-2 flex gap-2">
        <Link
          to="/tasks/$taskId"
          params={{ taskId: card.taskId }}
          aria-label={`Open ${card.title}`}
          className="rounded-md border border-line px-2 py-0.5 text-xs hover:bg-canvas"
        >
          Open
        </Link>
        {canCancel(card) && (
          <button
            type="button"
            disabled={cancelling}
            aria-label={`Cancel ${card.title}`}
            onClick={() => {
              onCancel(card.taskId);
            }}
            className="rounded-md border border-line px-2 py-0.5 text-xs hover:bg-canvas disabled:opacity-50"
          >
            {cancelling ? "Cancelling…" : "Cancel"}
          </button>
        )}
      </div>
    </li>
  );
}
