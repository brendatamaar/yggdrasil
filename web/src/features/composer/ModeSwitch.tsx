import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { describeError } from "../../api/client";
import { taskKeys } from "../../api/queries";
import { sendMessage } from "./api";
import { useWriteModesOff } from "./agents";
import { MODE_LABEL, needsOptIn, switchTargets, type Mode } from "./core";
import { OptInDialog } from "./OptInDialog";
import { useComposerUi } from "./store";

/**
 * Next message of an idle task, with the mode for this turn: Ask <-> the
 * task's own write mode, nothing else.
 */
export function MessageComposer({
  taskId,
  currentMode,
  writeMode,
}: {
  taskId: string;
  currentMode: Mode;
  /** The task's write mode (its mode when it was created as a write task). */
  writeMode?: Mode;
}) {
  const qc = useQueryClient();
  const { writeModesOff, acknowledged, acknowledge } = useComposerUi();
  const [prompt, setPrompt] = useState("");
  const [mode, setMode] = useState<Mode>(currentMode);
  const [confirming, setConfirming] = useState(false);
  const healthOff = useWriteModesOff();
  const targets = writeModesOff || healthOff ? [] : switchTargets(currentMode, writeMode);
  const choices: Mode[] = [currentMode, ...targets];

  const send = useMutation({
    mutationFn: (optIn: boolean) => sendMessage(taskId, { mode, prompt, ...(optIn ? { optIn: true } : {}) }),
    onSuccess: async () => {
      setPrompt("");
      await qc.invalidateQueries({ queryKey: taskKeys.all });
    },
  });

  const submit = (confirmed: boolean) => {
    if (prompt.trim() === "" || send.isPending) return;
    if (needsOptIn(mode) && !confirmed && !acknowledged.includes(mode)) {
      setConfirming(true);
      return;
    }
    send.mutate(needsOptIn(mode));
  };

  return (
    <form
      aria-label="Next message"
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        submit(false);
      }}
    >
      <label htmlFor="next-message" className="sr-only">
        Next message
      </label>
      <textarea
        id="next-message"
        className="min-h-16 w-full rounded border border-line bg-canvas px-2 py-1 text-sm"
        value={prompt}
        onChange={(e) => { setPrompt(e.target.value); }}
      />
      <div className="flex flex-wrap items-center gap-3">
        {choices.length > 1 ? (
          <fieldset className="flex items-center gap-2 text-sm">
            <legend className="sr-only">Mode for this message</legend>
            {choices.map((c) => (
              <label key={c} className="flex items-center gap-1">
                <input type="radio" name="turn-mode" checked={mode === c} onChange={() => { setMode(c); }} />
                {MODE_LABEL[c]}
              </label>
            ))}
          </fieldset>
        ) : null}
        <button
          type="submit"
          disabled={prompt.trim() === "" || send.isPending}
          className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
        >
          Send
        </button>
      </div>
      {send.error ? <p role="alert" className="text-sm text-danger">{describeError(send.error)}</p> : null}
      <OptInDialog
        mode={confirming ? mode : null}
        onCancel={() => { setConfirming(false); }}
        onConfirm={() => { acknowledge(mode); setConfirming(false); send.mutate(true); }}
      />
    </form>
  );
}
