import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useRef, useState, type SyntheticEvent } from "react";
import { ApiError, describeError } from "../../api/client";
import { createTask, taskKeys } from "../../api/queries";
import { AGENTS } from "./agents";
import {
  effectiveScope,
  invalidReason,
  isValidModelName,
  isWriteModesRefusal,
  MODE_HINT,
  MODE_LABEL,
  stepSubmit,
  visibleModes,
  type Mode,
  type Scope,
  type SubmitCommand,
  type SubmitInput,
  type SubmitState,
} from "./core";
import { useComposerRepos, useModels } from "./hooks";
import { MentionTextarea } from "./MentionTextarea";
import { OptInDialog } from "./OptInDialog";
import { SavedPromptsMenu } from "./SavedPromptsMenu";
import { useComposerUi } from "./store";

const STORAGE_KEY = "ygg.composer";

// Only the two IDs are remembered, never the prompt.
function loadIds(): { projectId: string; repoId: string } {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "null");
    if (typeof v === "object" && v !== null) {
      const o = v as Record<string, unknown>;
      return {
        projectId: typeof o.projectId === "string" ? o.projectId : "",
        repoId: typeof o.repoId === "string" ? o.repoId : "",
      };
    }
  } catch {
    // Unreadable: start empty.
  }
  return { projectId: "", repoId: "" };
}

const field = "w-full rounded border border-line bg-canvas px-2 py-1 text-sm";
const CUSTOM = "__custom__";

/** Starts a task (AG-13): scope, repo, agent, model, mode, saved prompts and @ mentions. */
export function Composer({
  prompt: controlledPrompt,
  onPromptChange,
}: {
  /** Optional: lets a parent (the Home suggestions) fill the prompt. */
  prompt?: string;
  onPromptChange?: (prompt: string) => void;
} = {}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const ui = useComposerUi();
  const [ownPrompt, setOwnPrompt] = useState("");
  const prompt = controlledPrompt ?? ownPrompt;
  const setPrompt = (value: string) => {
    setOwnPrompt(value);
    onPromptChange?.(value);
  };
  const [title, setTitle] = useState("");
  const [ids, setIds] = useState(loadIds);
  const [scope, setScope] = useState<Scope>("repo");
  const [mode, setMode] = useState<Mode>("ask");
  const [agent, setAgent] = useState("mock");
  const [modelChoice, setModelChoice] = useState("");
  const [customModel, setCustomModel] = useState("");

  const repos = useComposerRepos();
  const projects = repos.data ?? [];
  const projectId = projects.some((p) => p.id === ids.projectId) ? ids.projectId : (projects[0]?.id ?? "");
  const project = projects.find((p) => p.id === projectId);
  const repoList = project?.repos ?? [];
  const repoId = repoList.some((r) => r.id === ids.repoId) ? ids.repoId : (repoList[0]?.id ?? "");
  const models = useModels(agent);

  const agentChoice = AGENTS.find((a) => a.id === agent);
  const model = modelChoice === CUSTOM ? customModel.trim() : modelChoice;
  const modes = visibleModes(!ui.writeModesOff);
  const activeMode: Mode = modes.includes(mode) ? mode : "ask";
  const scopeNow = effectiveScope(activeMode, scope);

  const reason = invalidReason({
    prompt,
    mode: activeMode,
    scope,
    projectId,
    repoId,
    agent,
    agentAvailable: agentChoice?.available ?? false,
    model,
  });

  const create = useMutation({
    // The title is what the person typed. The server never makes one from the
    // prompt, because lists and notifications show titles.
    mutationFn: (optIn: boolean) =>
      createTask({
        projectId,
        repoIds: scopeNow === "repo" ? [repoId] : [],
        scope: scopeNow,
        prompt,
        mode: activeMode,
        agent,
        ...(optIn ? { optIn: true } : {}),
        ...(model ? { model } : {}),
        ...(title.trim() ? { title: title.trim() } : {}),
      }),
  });

  const [state, setState] = useState<SubmitState>({ tag: "editing" });
  const stateRef = useRef<SubmitState>(state);

  const run = (c: SubmitCommand) => {
    if (c.tag === "acknowledge") ui.acknowledge(c.mode);
    else if (c.tag === "hideWriteModes") {
      ui.hideWriteModes();
      setMode("ask");
    } else {
      create.mutate(c.optIn, {
        onSuccess: (task) => {
          localStorage.setItem(STORAGE_KEY, JSON.stringify({ projectId, repoId }));
          send({ tag: "succeeded" });
          void queryClient.invalidateQueries({ queryKey: taskKeys.all });
          void navigate({ to: "/tasks/$taskId", params: { taskId: task.id } });
        },
        onError: (err) => {
          const refused = err instanceof ApiError && isWriteModesRefusal(err.status, err.message);
          send({ tag: "failed", message: describeError(err), writeModesOff: refused });
        },
      });
    }
  };
  const send = (input: SubmitInput) => {
    const [next, commands] = stepSubmit(stateRef.current, input);
    stateRef.current = next;
    setState(next);
    commands.forEach(run);
  };

  const submit = (e: SyntheticEvent) => {
    e.preventDefault();
    if (reason !== null) return;
    send({ tag: "submit", mode: activeMode, acknowledged: ui.acknowledged.includes(activeMode) });
  };

  const noProjects = repos.isSuccess && projects.length === 0;
  const modelsList = models.data ?? [];

  return (
    <form onSubmit={submit} className="mx-auto max-w-3xl space-y-3" aria-label="New task">
      <div>
        <label htmlFor="task-title" className="mb-1 block text-sm">
          Task name (optional)
        </label>
        <input
          id="task-title"
          className={field}
          maxLength={80}
          placeholder="Shown in lists and notifications"
          value={title}
          onChange={(e) => {
            setTitle(e.target.value);
          }}
        />
      </div>
      <div>
        <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
          <label htmlFor="prompt" className="text-sm font-medium">
            What should the agent look into?
          </label>
          <SavedPromptsMenu text={prompt} onInsert={setPrompt} />
        </div>
        <MentionTextarea id="prompt" className={`${field} min-h-24`} value={prompt} onChange={setPrompt} projectId={projectId} />
        <p className="mt-1 text-xs text-muted">Type @ to add a file or folder. Ctrl+Enter starts the task.</p>
      </div>

      {repos.isPending ? <p className="text-sm text-muted">Loading projects...</p> : null}
      {repos.isError ? (
        <p role="alert" className="text-sm text-danger">
          {describeError(repos.error)}
        </p>
      ) : null}
      {noProjects ? (
        <p className="text-sm">
          No projects yet.{" "}
          <Link to="/projects" className="underline">
            Add a project
          </Link>{" "}
          to start a task.
        </p>
      ) : null}

      <div className="grid gap-3 sm:grid-cols-3">
        <div>
          <label htmlFor="project-id" className="mb-1 block text-sm">
            Project
          </label>
          <select
            id="project-id"
            className={field}
            value={projectId}
            disabled={projects.length === 0}
            onChange={(e) => {
              setIds({ projectId: e.target.value, repoId: "" });
            }}
          >
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="scope" className="mb-1 block text-sm">
            Scope
          </label>
          <select
            id="scope"
            className={field}
            value={scopeNow}
            disabled={activeMode === "project"}
            onChange={(e) => {
              setScope(e.target.value === "project" ? "project" : "repo");
            }}
          >
            <option value="repo">One repo</option>
            <option value="project">Whole project</option>
          </select>
        </div>
        <div>
          <label htmlFor="repo-id" className="mb-1 block text-sm">
            Repo
          </label>
          <select
            id="repo-id"
            className={field}
            value={repoId}
            disabled={scopeNow === "project" || repoList.length === 0}
            onChange={(e) => {
              setIds((s) => ({ ...s, repoId: e.target.value }));
            }}
          >
            {repoList.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="agent" className="mb-1 block text-sm">
            Agent
          </label>
          <select
            id="agent"
            className={field}
            value={agent}
            onChange={(e) => {
              setAgent(e.target.value);
              setModelChoice("");
            }}
          >
            {AGENTS.map((a) => (
              <option key={a.id} value={a.id} disabled={!a.available}>
                {a.label}
                {a.available ? "" : ` (unavailable: ${a.reason ?? "not set up"})`}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="model" className="mb-1 block text-sm">
            Model
          </label>
          <select
            id="model"
            className={field}
            value={modelChoice}
            onChange={(e) => {
              setModelChoice(e.target.value);
            }}
          >
            <option value="">Agent default</option>
            {modelsList.map((m) => (
              <option key={m.id} value={m.id}>
                {m.label}
              </option>
            ))}
            <option value={CUSTOM}>Custom...</option>
          </select>
          {models.isPending ? <p className="mt-1 text-xs text-muted">Loading models...</p> : null}
          {models.isError ? (
            <p className="mt-1 text-xs text-muted">Models could not be loaded; use the default or a custom name.</p>
          ) : null}
          {models.isSuccess && modelsList.length === 0 ? (
            <p className="mt-1 text-xs text-muted">This agent lists no models.</p>
          ) : null}
        </div>
        {modelChoice === CUSTOM ? (
          <div>
            <label htmlFor="custom-model" className="mb-1 block text-sm">
              Custom model name
            </label>
            <input
              id="custom-model"
              className={field}
              maxLength={64}
              value={customModel}
              aria-invalid={customModel !== "" && !isValidModelName(customModel.trim())}
              onChange={(e) => {
                setCustomModel(e.target.value);
              }}
            />
          </div>
        ) : null}
      </div>

      <fieldset className="flex flex-wrap items-center gap-3 text-sm">
        <legend className="mb-1 text-sm">Mode</legend>
        {modes.map((m) => (
          <label key={m} className="flex items-center gap-1">
            <input
              type="radio"
              name="mode"
              value={m}
              checked={activeMode === m}
              onChange={() => {
                setMode(m);
              }}
            />
            {MODE_LABEL[m]}
          </label>
        ))}
      </fieldset>
      <p className={`text-xs ${activeMode === "current_branch" || activeMode === "project" ? "text-danger" : "text-muted"}`}>
        {MODE_HINT[activeMode]}
      </p>

      <div className="flex flex-wrap items-center gap-3">
        <button
          type="submit"
          disabled={reason !== null || state.tag === "submitting"}
          aria-describedby="start-reason"
          className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
        >
          Start task
        </button>
        <span id="start-reason" className="text-xs text-muted">
          {reason ?? ""}
        </span>
      </div>
      {state.tag === "failed" ? (
        <p role="alert" className="text-sm text-danger">
          {state.message}
        </p>
      ) : null}
      <OptInDialog
        mode={state.tag === "confirming" ? state.mode : null}
        onConfirm={() => {
          send({ tag: "confirm" });
        }}
        onCancel={() => {
          send({ tag: "cancel" });
        }}
      />
    </form>
  );
}
