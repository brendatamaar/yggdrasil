import { apiFetch } from "./client";
import type { SearchRequest, SearchResponse } from "./gen/api.gen";

export const searchCode = (req: SearchRequest) =>
  apiFetch<SearchResponse>("/api/search", { method: "POST", body: JSON.stringify(req) });
