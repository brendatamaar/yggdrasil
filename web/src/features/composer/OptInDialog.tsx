import * as Dialog from "@radix-ui/react-dialog";
import { useId, useState } from "react";
import { OPT_IN_TEXT, type Mode } from "./core";

/**
 * Warning before the first Current-branch or Whole-project submit. Acts as an
 * alert dialog: focus is trapped, Escape cancels, and focus starts on Cancel
 * so Enter never confirms by accident.
 *
 * With `requireCheck` the person must also tick a box before the confirm
 * button works: used when the consent travels with a click ("Send failure to
 * agent"), where the warning is new to the person at that moment.
 */
export function OptInDialog({
  mode,
  onConfirm,
  onCancel,
  requireCheck = false,
  confirmLabel = "I understand, start the task",
  checkLabel = "I understand and want to continue.",
}: {
  mode: Mode | null;
  onConfirm: () => void;
  onCancel: () => void;
  requireCheck?: boolean;
  confirmLabel?: string;
  checkLabel?: string;
}) {
  const text = mode === "current_branch" || mode === "project" ? OPT_IN_TEXT[mode] : null;
  return (
    <Dialog.Root
      open={text !== null}
      onOpenChange={(o) => {
        if (!o) onCancel();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 bg-black/50" />
        <Dialog.Content
          role="alertdialog"
          className="fixed top-1/2 left-1/2 w-[30rem] max-w-[95vw] -translate-x-1/2 -translate-y-1/2 space-y-3 rounded-md border-2 border-danger bg-canvas p-4 shadow-lg"
          onPointerDownOutside={(e) => {
            e.preventDefault();
          }}
          onOpenAutoFocus={(e) => {
            e.preventDefault();
            document.getElementById("optin-cancel")?.focus();
          }}
        >
          <Dialog.Title className="text-base font-semibold">{text?.title}</Dialog.Title>
          <Dialog.Description className="text-sm">{text?.body}</Dialog.Description>
          <Actions requireCheck={requireCheck} confirmLabel={confirmLabel} checkLabel={checkLabel} onConfirm={onConfirm} onCancel={onCancel} />
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

// Its own component so the checkbox starts unticked every time the dialog opens.
function Actions({
  requireCheck,
  confirmLabel,
  checkLabel,
  onConfirm,
  onCancel,
}: {
  requireCheck: boolean;
  confirmLabel: string;
  checkLabel: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const [checked, setChecked] = useState(false);
  const id = useId();
  return (
    <>
      {requireCheck ? (
        <div className="flex items-center gap-2 text-sm">
          <input
            id={id}
            type="checkbox"
            checked={checked}
            onChange={(e) => {
              setChecked(e.target.checked);
            }}
          />
          <label htmlFor={id}>{checkLabel}</label>
        </div>
      ) : null}
      <div className="flex justify-end gap-2">
        <button id="optin-cancel" type="button" className="rounded border border-line px-3 py-1 text-sm" onClick={onCancel}>
          Cancel
        </button>
        <button
          type="button"
          className="rounded bg-danger px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
          disabled={requireCheck && !checked}
          onClick={onConfirm}
        >
          {confirmLabel}
        </button>
      </div>
    </>
  );
}
