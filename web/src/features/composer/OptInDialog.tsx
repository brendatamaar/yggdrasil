import * as Dialog from "@radix-ui/react-dialog";
import { OPT_IN_TEXT, type Mode } from "./core";

/**
 * Warning before the first Current-branch or Whole-project submit. Acts as an
 * alert dialog: focus is trapped, Escape cancels, and focus starts on Cancel
 * so Enter never confirms by accident.
 */
export function OptInDialog({
  mode,
  onConfirm,
  onCancel,
}: {
  mode: Mode | null;
  onConfirm: () => void;
  onCancel: () => void;
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
          <div className="flex justify-end gap-2">
            <button id="optin-cancel" type="button" className="rounded border border-line px-3 py-1 text-sm" onClick={onCancel}>
              Cancel
            </button>
            <button
              type="button"
              className="rounded bg-danger px-3 py-1 text-sm font-medium text-canvas"
              onClick={onConfirm}
            >
              I understand, start the task
            </button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
