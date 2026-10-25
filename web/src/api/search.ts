import { apiFetch } from "./client";
import type { SearchRequest, SearchResponse } from "./gen/api.gen";

export const searchCode = (req: SearchRequest, signal?: AbortSignal) =>
  apiFetch<SearchResponse>("/api/search", {
    method: "POST",
    body: JSON.stringify(req),
    ...(signal ? { signal } : {}),
  });
