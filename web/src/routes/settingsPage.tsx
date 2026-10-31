import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiFetch, describeError } from "../api/client";
import type { MaxAgentsResponse, SetMaxAgentsRequest } from "../api/gen/api.gen";
import { boardKeys, useBoard } from "../features/board";
import { maxAgentsError, roomHintText, usageLine, usageTotals } from "./settingsModel";

const maxAgentsKey = ["settings", "max-agents"] as const;
const field = "w-20 rounded border border-line bg-canvas px-2 py-1 text-sm";

/** Settings: how many agents run at once (AG-11) and what they used (AG-15). */
export function SettingsPage() {
  const qc = useQueryClient();
  const max = useQuery({ queryKey: maxAgentsKey, queryFn: () => apiFetch<MaxAgentsResponse>("/api/board/max-agents") });
  const board = useBoard();
  const [typed, setTyped] = useState<string | null>(null);
  const save = useMutation({
    mutationFn: (n: number) =>
      apiFetch<MaxAgentsResponse>("/api/board/max-agents", {
        method: "PUT",
        body: JSON.stringify({ maxAgents: n } satisfies SetMaxAgentsRequest),
      }),
    onSuccess: async () => {
      setTyped(null);
      await Promise.all([
        qc.invalidateQueries({ queryKey: maxAgentsKey }),
        qc.invalidateQueries({ queryKey: boardKeys.all }),
      ]);
    },
  });

  const m = max.data;
  const value = typed ?? (m ? String(m.maxAgents) : "");
  const invalid = m ? maxAgentsError(value, m) : null;
  const hint = m ? roomHintText(m.hint, m.maxAgents) : null;

  return (
    <section aria-labelledby="page-title" className="mx-auto max-w-xl space-y-6">
      <h1 id="page-title" className="text-lg font-semibold">
        Settings
      </h1>

      <form
        aria-label="Agents at once"
        className="space-y-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (m && !invalid) save.mutate(Number(value));
        }}
      >
        <h2 className="font-medium">Agents at once</h2>
        {max.error ? (
          <p role="alert" className="text-sm text-danger">
            {describeError(max.error)}
          </p>
        ) : null}
        <div className="flex items-center gap-2">
          <label htmlFor="max-agents" className="text-sm">
            Run up to
          </label>
          <input
            id="max-agents"
            type="number"
            inputMode="numeric"
            className={field}
            value={value}
            min={m?.min}
            max={m?.max}
            disabled={!m}
            aria-invalid={invalid !== null}
            aria-describedby="max-agents-help"
            onChange={(e) => {
              setTyped(e.target.value);
            }}
          />
          <span className="text-sm">agents</span>
          <button
            type="submit"
            disabled={!m || invalid !== null || save.isPending || value === String(m.maxAgents)}
            className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
          >
            Save
          </button>
        </div>
        <p id="max-agents-help" className="text-sm text-muted">
          {invalid ?? "Other tasks wait in the queue until a slot is free."}
        </p>
        {hint && m ? (
          <p className="text-sm">
            {hint}{" "}
            <button
              type="button"
              className="underline"
              onClick={() => {
                setTyped(String(m.hint.raiseTo));
              }}
            >
              Use {m.hint.raiseTo}
            </button>
          </p>
        ) : null}
        {save.error ? (
          <p role="alert" className="text-sm text-danger">
            {describeError(save.error)}
          </p>
        ) : null}
      </form>

      <div className="space-y-1">
        <h2 className="font-medium">Usage</h2>
        {board.error ? (
          <p role="alert" className="text-sm text-danger">
            {describeError(board.error)}
          </p>
        ) : (
          <p className="text-sm" data-testid="usage-line">
            {board.data ? usageLine(usageTotals(board.data.cards)) : "Loading…"}
          </p>
        )}
      </div>
    </section>
  );
}
