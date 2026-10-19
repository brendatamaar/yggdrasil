import { apiFetch } from "./client";
import type { HostsResponse, InboxResponse, RefreshHostsRequest } from "./gen/api.gen";

export const fetchInbox = () => apiFetch<InboxResponse>("/api/mrs");
export const fetchHostConfig = () => apiFetch<HostsResponse>("/api/hosts");
export const refreshInbox = (req: RefreshHostsRequest = { repos: [], instances: [] }) =>
  apiFetch<undefined>("/api/hosts/refresh", { method: "POST", body: JSON.stringify(req) });
