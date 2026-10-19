import type { StepSpecDTO } from "../../api/gen/api.gen";
import { joinArgs, splitArgs } from "./ui";

export interface Row {
  repoId: string;
  name: string;
  command: string;
}

export const emptyRow = (repoId = ""): Row => ({ repoId, name: "", command: "" });

export const rowsFromSteps = (steps: StepSpecDTO[]): Row[] =>
  steps.map((s) => ({ repoId: s.repoId, name: s.name, command: joinArgs(s.argv) }));

export const rowsToSteps = (rows: Row[]): StepSpecDTO[] =>
  rows.map((r) => ({ repoId: r.repoId, name: r.name.trim(), argv: splitArgs(r.command) }));

export const rowsValid = (rows: Row[]) =>
  rows.length > 0 && rows.every((r) => r.repoId && r.name.trim() && splitArgs(r.command).length > 0);

