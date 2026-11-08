import { useState, type ReactNode } from "react";
import { ApiError } from "../../api/client";
import { OptInDialog, needsOptIn, type Mode } from "../composer";

/** True for the 409 that says the mode needs the opt-in and the request had none. */
export const isNeedsOptIn = (e: unknown) => e instanceof ApiError && e.status === 409 && e.code === "needs_opt_in";

/**
 * The consent travels with the click. For a mode that needs the opt-in,
 * `request` opens the warning first and runs the send with `true` only after
 * the person ticks the box and confirms; for other modes it runs at once with
 * `false`. `retry` opens the same dialog after a server 409 needs_opt_in.
 */
export function useOptInGate(mode: Mode | undefined): {
  request: (run: (optIn: boolean) => void) => void;
  retry: (run: (optIn: boolean) => void) => void;
  dialog: ReactNode;
} {
  const [pending, setPending] = useState<((optIn: boolean) => void) | null>(null);
  const open = (run: (optIn: boolean) => void) => {
    setPending(() => run);
  };
  return {
    request: (run) => {
      if (mode !== undefined && needsOptIn(mode)) open(run);
      else run(false);
    },
    retry: open,
    dialog: (
      <OptInDialog
        mode={pending ? (mode !== undefined && needsOptIn(mode) ? mode : "project") : null}
        requireCheck
        confirmLabel="Send to the agent"
        checkLabel="I understand the warning above and want to send."
        onCancel={() => {
          setPending(null);
        }}
        onConfirm={() => {
          const run = pending;
          setPending(null);
          run?.(true);
        }}
      />
    ),
  };
}
