import { apiFetch } from "../../api/client";
import type {
  DraftRequest,
  DraftResponse,
  MentionsResponse,
  MessageRequest,
  ModelsResponse,
  SavedPromptDTO,
  SavedPromptsResponse,
  SavePromptRequest,
  TaskResponse,
} from "../../api/gen/api.gen";

const enc = encodeURIComponent;
const post = (body: unknown, signal?: AbortSignal): RequestInit => ({
  method: "POST",
  body: JSON.stringify(body),
  ...(signal ? { signal } : {}),
});

export const fetchPrompts = async (): Promise<SavedPromptDTO[]> =>
  (await apiFetch<SavedPromptsResponse>("/api/composer/prompts")).prompts;
export const savePrompt = (req: SavePromptRequest) => apiFetch<SavedPromptDTO>("/api/composer/prompts", post(req));
export const deletePrompt = (id: string) =>
  apiFetch<unknown>(`/api/composer/prompts/${enc(id)}`, { method: "DELETE" });
export const fetchModels = (agent: string) => apiFetch<ModelsResponse>(`/api/composer/models?agent=${enc(agent)}`);
export const fetchMentions = (project: string, q: string, signal: AbortSignal) =>
  apiFetch<MentionsResponse>(`/api/composer/mentions?project=${enc(project)}&q=${enc(q)}`, { signal });
export const requestDraft = (req: DraftRequest, signal: AbortSignal) =>
  apiFetch<DraftResponse>("/api/composer/drafts", post(req, signal));
export const sendMessage = (taskId: string, req: MessageRequest) =>
  apiFetch<TaskResponse>(`/api/tasks/${enc(taskId)}/messages`, post(req));
