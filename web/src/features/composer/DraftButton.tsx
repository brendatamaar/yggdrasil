import { useRef, useState } from "react";
import { describeError } from "../../api/client";
import { requestDraft } from "./api";
import { stepDraft, type DraftCommand, type DraftInput, type DraftState } from "./core";

export const DRAFT_TIMEOUT_MS = 120_000;

const LABEL = { commit_message: "Draft commit message", mr_description: "Draft MR description" } as const;

/**
 * Asks the agent to write a commit message or MR description and hands the
 * text to `onDraft`. It fills a field; it never submits anything.
 */
export function DraftButton({
  kind,
  projectId,
  repoId,
  taskId,
  onDraft,
}: {
  kind: keyof typeof LABEL;
  projectId: string;
  repoId: string;
  taskId?: string;
  onDraft: (text: string) => void;
}) {
  const [state, setState] = useState<DraftState>({ tag: "idle" });
  const stateRef = useRef<DraftState>(state);
  const abort = useRef<AbortController | null>(null);
  const nextId = useRef(0);

  const send = (input: DraftInput, text?: string) => {
    const [next, commands] = stepDraft(stateRef.current, input);
    stateRef.current = next;
    setState(next);
    for (const c of commands) run(c, text);
  };

  const run = (c: DraftCommand, text?: string) => {
    if (c.tag === "abort") abort.current?.abort();
    else if (c.tag === "fill") onDraft(text ?? "");
    else {
      const ctl = new AbortController();
      abort.current = ctl;
      let timedOut = false;
      const timer = setTimeout(() => {
        timedOut = true;
        ctl.abort();
      }, DRAFT_TIMEOUT_MS);
      // projectId scopes the repo; the server resolves the repo from repoId.
      void requestDraft({ kind, repoId, ...(taskId ? { taskId } : {}) }, ctl.signal)
        .then((r) => {
          // fill is a command of the step; its text is the answer.
          send({ tag: "done", requestId: c.requestId }, r.text);
        })
        .catch((err: unknown) => {
          // After a cancel the step is idle, so it drops this failure.
          const message = timedOut ? "The draft took too long. Try again." : describeError(err);
          send({ tag: "failed", requestId: c.requestId, message });
        })
        .finally(() => {
          clearTimeout(timer);
        });
    }
  };

  const disabled = projectId === "" || repoId === "";
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      {state.tag === "loading" ? (
        <>
          <span role="status" className="text-xs text-muted">
            Drafting...
          </span>
          <button
            type="button"
            className="rounded border border-line px-2 py-0.5 text-xs"
            onClick={() => {
              send({ tag: "cancel" });
            }}
          >
            Cancel draft
          </button>
        </>
      ) : (
        <button
          type="button"
          disabled={disabled}
          title={disabled ? "Pick a repo first." : undefined}
          className="rounded border border-line px-2 py-0.5 text-xs disabled:opacity-50"
          onClick={() => {
            nextId.current += 1;
            send({ tag: "start", requestId: nextId.current });
          }}
        >
          {LABEL[kind]}
        </button>
      )}
      {state.tag === "failed" ? (
        <span role="alert" className="text-xs text-danger">
          {state.message}
        </span>
      ) : null}
    </span>
  );
}
