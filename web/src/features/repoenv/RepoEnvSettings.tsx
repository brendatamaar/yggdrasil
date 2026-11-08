import { useEffect, useId, useRef, useState } from "react";
import { describeError } from "../../api/client";
import { draftProblem, formFrom, formStep, fromDraft, pathHint, type FormInput, type FormState } from "./core";
import { useRepoEnv, useSaveRepoEnv } from "./hooks";
import type { RepoEnvDTO } from "../../api/gen/api.gen";

const field = "rounded border border-line bg-canvas px-2 py-1 text-sm";
const btn = "rounded border border-line px-2 py-0.5 text-sm hover:bg-surface disabled:opacity-50";

/** Repo environment settings: profile files, agent copy and compose (WE-02, WE-03). */
export function RepoEnvSettings({ repoId }: { repoId: string }) {
  const q = useRepoEnv(repoId);
  if (q.isPending) return <p className="text-sm text-muted">Loading…</p>;
  if (q.isError) {
    return (
      <p role="alert" className="text-sm text-danger">
        {describeError(q.error)}
      </p>
    );
  }
  return <EnvForm repoId={repoId} env={q.data} />;
}

function EnvForm({ repoId, env }: { repoId: string; env: RepoEnvDTO }) {
  const save = useSaveRepoEnv();
  const [form, setForm] = useState<FormState>(() => formFrom(env));
  const latest = useRef(form);
  const [newPath, setNewPath] = useState("");
  const [pathError, setPathError] = useState<string | null>(null);
  const ids = useId();

  const send = (input: FormInput) => {
    const next = formStep(latest.current, input);
    latest.current = next.state;
    setForm(next.state);
    for (const c of next.commands) {
      save.mutate(fromDraft(repoId, c.draft), {
        onSuccess: (saved) => {
          send({ tag: "saved", env: saved });
        },
        onError: (e) => {
          send({ tag: "saveFailed", message: describeError(e) });
        },
      });
    }
  };

  // Fresh server data replaces the form only when nothing is being edited.
  useEffect(() => {
    const next = formStep(latest.current, { tag: "reload", env });
    latest.current = next.state;
    setForm(next.state);
  }, [env]);

  const d = form.draft;
  const saving = form.tag === "saving";
  const problem = draftProblem(d);

  const addPath = () => {
    const hint = pathHint(newPath, d.profilePaths);
    setPathError(hint);
    if (hint) return;
    send({ tag: "edit", patch: { profilePaths: [...d.profilePaths, newPath.trim()] } });
    setNewPath("");
  };

  return (
    <form
      aria-label="Environment settings"
      className="space-y-5"
      onSubmit={(e) => {
        e.preventDefault();
        send({ tag: "save" });
      }}
    >
      <fieldset className="space-y-2" disabled={saving}>
        <legend className="font-medium">Profile files</legend>
        <p className="text-sm text-muted">
          Files such as .env that a new worktree needs. They are copied from the main checkout into a worktree. Existing
          files are never overwritten.
        </p>
        {d.profilePaths.length === 0 ? <p className="text-sm text-muted">No files listed.</p> : null}
        <ul aria-label="Profile files" className="space-y-1">
          {d.profilePaths.map((p) => (
            <li key={p} className="flex items-center gap-2">
              <code className="text-sm">{p}</code>
              <button
                type="button"
                className={btn}
                aria-label={`Remove ${p}`}
                onClick={() => {
                  send({ tag: "edit", patch: { profilePaths: d.profilePaths.filter((x) => x !== p) } });
                }}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
        <div className="flex items-center gap-2">
          <label htmlFor={`${ids}-path`} className="text-sm">
            Add a file
          </label>
          <input
            id={`${ids}-path`}
            className={`${field} w-56`}
            value={newPath}
            placeholder=".env"
            aria-invalid={pathError !== null}
            aria-describedby={pathError ? `${ids}-path-err` : undefined}
            onChange={(e) => {
              setNewPath(e.target.value);
              setPathError(null);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addPath();
              }
            }}
          />
          <button type="button" className={btn} onClick={addPath}>
            Add
          </button>
        </div>
        {pathError ? (
          <p id={`${ids}-path-err`} role="alert" className="text-sm text-danger">
            {pathError}
          </p>
        ) : null}
      </fieldset>

      <fieldset className="space-y-1" disabled={saving}>
        <legend className="font-medium">Agent tasks</legend>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={d.agentCopy}
            onChange={(e) => {
              send({ tag: "edit", patch: { agentCopy: e.target.checked } });
            }}
          />
          Also copy for agent tasks
        </label>
        <p className="text-sm text-warn">
          Warning: agents can read these files. They often hold secrets. Leave this off unless the task needs them.
        </p>
      </fieldset>

      <fieldset className="space-y-2" disabled={saving}>
        <legend className="font-medium">Docker Compose</legend>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={d.composeEnabled}
            onChange={(e) => {
              send({ tag: "edit", patch: { composeEnabled: e.target.checked } });
            }}
          />
          Run a compose project for each worktree
        </label>
        {d.composeEnabled ? (
          <div className="flex items-center gap-2">
            <label htmlFor={`${ids}-compose`} className="text-sm">
              Compose file
            </label>
            <input
              id={`${ids}-compose`}
              className={`${field} w-72`}
              value={d.composeFile}
              placeholder="docker-compose.yml"
              onChange={(e) => {
                send({ tag: "edit", patch: { composeFile: e.target.value } });
              }}
            />
          </div>
        ) : null}
        <p className="text-sm text-muted">
          Each worktree gets its own project. You confirm the compose file before it first runs and again whenever it
          changes.
        </p>
      </fieldset>

      {problem ? <p className="text-sm text-warn">{problem}</p> : null}
      {form.tag === "failed" ? (
        <p role="alert" className="text-sm text-danger">
          {form.message}
        </p>
      ) : null}
      <div className="flex items-center gap-3">
        <button type="submit" className={btn} disabled={saving || problem !== null || (form.tag === "editing" && !form.dirty)}>
          {saving ? "Saving…" : "Save"}
        </button>
        {form.tag === "editing" && !form.dirty && save.isSuccess ? (
          <span role="status" className="text-sm text-muted">
            Saved
          </span>
        ) : null}
      </div>
    </form>
  );
}
