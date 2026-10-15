import { QueryClient } from "@tanstack/react-query";
import type { ErrorBody } from "./gen/api.gen";

/** An error response from the server, carrying the F4 error body (SF-01). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly hint?: string;

  constructor(status: number, body: ErrorBody) {
    super(body.message);
    this.name = "ApiError";
    this.status = status;
    this.code = body.code;
    this.hint = body.hint;
  }
}

const unreachable = "Can't reach Yggdrasil. Check that it is still running, then try again.";

function isErrorBody(value: unknown): value is ErrorBody {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  return typeof v.code === "string" && typeof v.message === "string";
}

/** Plain-language text for any thrown value: message plus the hint, when there is one. */
export function describeError(error: unknown): string {
  if (error instanceof ApiError) return error.hint ? `${error.message} ${error.hint}` : error.message;
  return "Something went wrong. Try again.";
}

/** Fetches `path` and parses JSON as T. Throws ApiError for error responses. */
export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  headers.set("Accept", "application/json");
  if (init?.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");

  let res: Response;
  try {
    res = await fetch(path, { ...init, headers });
  } catch {
    throw new ApiError(0, { code: "unavailable", message: unreachable });
  }

  if (!res.ok) {
    const body: unknown = await res.json().catch(() => null);
    throw new ApiError(
      res.status,
      isErrorBody(body)
        ? body
        : { code: "internal", message: "The server sent an unexpected response.", hint: "Try again." },
    );
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // Server errors are answers, not glitches; only retry when the server was unreachable.
        retry: (count, error) => (error instanceof ApiError ? error.status === 0 && count < 2 : count < 2),
        refetchOnWindowFocus: false,
      },
    },
  });
}
