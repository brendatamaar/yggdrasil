import * as Dialog from "@radix-ui/react-dialog";
import { useState } from "react";
import { enqueueBuild } from "../../api/builds";
import { describeError } from "../../api/client";
import type { BuildPresetDTO, RepoStatusDTO } from "../../api/gen/api.gen";
import { DialogShell } from "../repos/AddProjectDialog";
import { btn, btnPrimary } from "../repos/ui";
import { useBuildMutation } from "./hooks";
import { emptyRow, rowsFromSteps, rowsToSteps, rowsValid, type Row } from "./rows";
import { StepRows } from "./StepRows";
import { input } from "./ui";

export function NewBuildDialog({
  trigger,
  repos,
  presets,
  onStarted,
}: {
  trigger: React.ReactNode;
  repos: RepoStatusDTO[];
  presets: BuildPresetDTO[];
  onStarted?: (buildId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [rows, setRows] = useState<Row[]>([emptyRow()]);
  const [presetId, setPresetId] = useState("");
  const start = useBuildMutation(enqueueBuild);

  const pick = (id: string) => {
    setPresetId(id);
    const p = presets.find((x) => x.id === id);
    if (p) setRows(rowsFromSteps(p.steps));
  };

  return (
    <Dialog.Root
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) start.reset();
      }}
    >
      <Dialog.Trigger asChild>{trigger}</Dialog.Trigger>
      <DialogShell title="New build" description="Runs in the main checkouts of the repos you pick, one step after another.">
        {presets.length > 0 && (
          <label className="flex items-center gap-2 text-sm">
            Preset
            <select className={input} value={presetId} onChange={(e) => { pick(e.target.value); }}>
              <option value="">None</option>
              {presets.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
        )}
        <StepRows rows={rows} repos={repos} onChange={setRows} />
        {start.isError && (
          <p role="alert" className="text-sm text-red-500">
            {describeError(start.error)}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Dialog.Close className={btn}>Cancel</Dialog.Close>
          <button
            type="button"
            className={btnPrimary}
            disabled={!rowsValid(rows) || start.isPending}
            onClick={() => {
              start.mutate(
                { target: "main", ...(presetId ? { presetId } : {}), steps: rowsToSteps(rows) },
                {
                  onSuccess: (r) => {
                    setOpen(false);
                    onStarted?.(r.buildId);
                  },
                },
              );
            }}
          >
            Start build
          </button>
        </div>
      </DialogShell>
    </Dialog.Root>
  );
}
