import { vi } from "vitest";

export interface Call {
  method: string;
  /** Path and query, as sent. */
  path: string;
  body?: unknown;
}

export interface Reply {
  status?: number;
  body: unknown;
}

export type Handler = (call: Call) => Reply | undefined;

const notFound: Reply = { status: 404, body: { code: "not_found", message: "That item doesn't exist (any more).", hint: "Refresh the page and try again." } };

/** What the server says when a test doesn't care: nothing running, no events. */
function defaultReply(call: Call): Reply {
  if (call.path.startsWith("/api/tasks?")) return { body: { tasks: [] } };
  if (call.path === "/api/health") return { body: { status: "ok", version: "test" } };
  if (/^\/api\/tasks\/[^/]+\/events/.test(call.path)) return { body: { events: [] } };
  return notFound;
}

/**
 * Replaces fetch with a fake server. `handler` answers first; whatever it
 * leaves to undefined gets a quiet default. Returns the calls made, in order.
 * The setup file restores fetch after each test.
 */
export function stubApi(handler?: Handler) {
  const calls: Call[] = [];
  const fn = vi.fn((input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const call: Call = {
      method: init?.method ?? "GET",
      path: typeof input === "string" ? input : input instanceof URL ? input.pathname + input.search : input.url,
      body: typeof init?.body === "string" ? (JSON.parse(init.body) as unknown) : undefined,
    };
    calls.push(call);
    const reply = handler?.(call) ?? defaultReply(call);
    return Promise.resolve(
      new Response(JSON.stringify(reply.body), {
        status: reply.status ?? 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  vi.stubGlobal("fetch", fn);
  return { calls, fn };
}
