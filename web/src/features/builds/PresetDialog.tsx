import * as Dialog from "@radix-ui/react-dialog";
import { useState } from "react";
import { createPreset, updatePreset } from "../../api/builds";
import { describeError } from "../../api/client";
import type { BuildPresetDTO, ProjectDTO } from "../../api/gen/api.gen";
import { DialogShell } from "../repos/AddProjectDialog";
import { btn, btnPrimary } from "../repos/ui";
import { buildKeys, useBuildMutation } from "./hooks";
import { emptyRow, rowsFromSteps, rowsToSteps, rowsValid, type Row } from "./rows";
import { StepRows } from "./StepRows";
import { input } from "./ui";

/** Creates a preset, or edits `preset` when given. */
export function PresetDialog({
  trigger,
  projects,
  preset,
}: {
  trigger: React.ReactNode;
  projects: ProjectDTO[];
  preset?: BuildPresetDTO;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(preset?.name ?? "");
  const [picked, setProjectId] = useState(preset?.projectId ?? "");
  const projectId = picked || (projects[0]?.id ?? "");
  const [rows, setRows] = useState<Row[]>(preset ? rowsFromSteps(preset.steps) : [emptyRow()]);
  const save = useBuildMutation(
    (v: { name: string; projectId: string; rows: Row[] }) =>
      preset
        ? updatePreset(preset.id, { name: v.name, steps: rowsToSteps(v.rows) })
        : createPreset({ projectId: v.projectId, name: v.name, steps: rowsToSteps(v.rows) }),
    buildKeys.presets,
  );
  const repos = (projects.find((p) => p.id === projectId) ?? projects[0])?.repos ?? [];

  return (
    <Dialog.Root
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) save.reset();
      }}
    >
      <Dialog.Trigger asChild>{trigger}</Dialog.Trigger>
      <DialogShell
        title={preset ? "Edit preset" : "New preset"}
        description="A preset is a saved list of steps you can run again."
      >
        <label className="flex items-center gap-2 text-sm">
          Name
          <input className={input} value={name} onChange={(e) => { setName(e.target.value); }} />
        </label>
        {!preset && (
          <label className="flex items-center gap-2 text-sm">
            Project
            <select className={input} value={projectId} onChange={(e) => { setProjectId(e.target.value); }}>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
        )}
        <StepRows rows={rows} repos={repos} onChange={setRows} />
        {save.isError && (
          <p role="alert" className="text-sm text-red-500">
            {describeError(save.error)}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Dialog.Close className={btn}>Cancel</Dialog.Close>
          <button
            type="button"
            className={btnPrimary}
            disabled={!name.trim() || !projectId || !rowsValid(rows) || save.isPending}
            onClick={() => {
              save.mutate({ name: name.trim(), projectId, rows }, { onSuccess: () => { setOpen(false); } });
            }}
          >
            Save preset
          </button>
        </div>
      </DialogShell>
    </Dialog.Root>
  );
}
