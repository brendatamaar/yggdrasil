import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState, type SyntheticEvent } from "react";
import { describeError } from "../api/client";
import { createTask, taskKeys } from "../api/queries";

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

/**
 * Starts a task. Only Ask mode (read-only) exists for now, and the repo is a
 * typed ID until the project pages can list repos.
 */
export function Composer() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [prompt, setPrompt] = useState("");
  const [title, setTitle] = useState("");
  const [{ projectId, repoId }, setIds] = useState(loadIds);
  const [agent, setAgent] = useState("mock");

  const create = useMutation({
    // The title is what the person typed. The server never makes one from the
    // prompt, because lists and notifications show titles.
    mutationFn: () =>
      createTask({ projectId, repoIds: [repoId], prompt, mode: "ask", agent, ...(title.trim() ? { title: title.trim() } : {}) }),
    onSuccess: async (task) => {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ projectId, repoId }));
      await queryClient.invalidateQueries({ queryKey: taskKeys.all });
      await navigate({ to: "/tasks/$taskId", params: { taskId: task.id } });
    },
  });

  const ready = prompt.trim() !== "" && projectId.trim() !== "" && repoId.trim() !== "" && !create.isPending;

  const submit = (e: SyntheticEvent) => {
    e.preventDefault();
    if (ready) create.mutate();
  };

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
        <label htmlFor="prompt" className="mb-1 block text-sm font-medium">
          What should the agent look into?
        </label>
        <textarea
          id="prompt"
          className={`${field} min-h-24`}
          value={prompt}
          onChange={(e) => {
            setPrompt(e.target.value);
          }}
        />
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        <div>
          <label htmlFor="project-id" className="mb-1 block text-sm">
            Project ID
          </label>
          <input
            id="project-id"
            className={field}
            value={projectId}
            onChange={(e) => {
              setIds((s) => ({ ...s, projectId: e.target.value }));
            }}
          />
        </div>
        <div>
          <label htmlFor="repo-id" className="mb-1 block text-sm">
            Repo ID
          </label>
          <input
            id="repo-id"
            className={field}
            value={repoId}
            onChange={(e) => {
              setIds((s) => ({ ...s, repoId: e.target.value }));
            }}
          />
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
            }}
          >
            <option value="mock">Demo agent</option>
          </select>
        </div>
      </div>
      <div className="flex items-center gap-3">
        <button
          type="submit"
          disabled={!ready}
          className="rounded bg-accent px-3 py-1 text-sm font-medium text-canvas disabled:opacity-50"
        >
          Start task
        </button>
        <span className="text-xs text-muted">Ask mode: the agent can read, not change.</span>
      </div>
      {create.error ? (
        <p role="alert" className="text-sm text-danger">
          {describeError(create.error)}
        </p>
      ) : null}
    </form>
  );
}
