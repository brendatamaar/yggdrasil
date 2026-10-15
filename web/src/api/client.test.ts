import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, apiFetch, describeError } from "./client";

function respond(status: number, body: unknown) {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(typeof body === "string" ? body : JSON.stringify(body), { status }))));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("apiFetch", () => {
  it("returns parsed JSON on success", async () => {
    respond(200, { status: "ok", version: "1" });
    expect(await apiFetch("/api/health")).toEqual({ status: "ok", version: "1" });
  });

  it("turns an ErrorBody into an ApiError with message and hint", async () => {
    respond(404, { code: "not_found", message: "That task does not exist.", hint: "Pick another task." });
    const err = await apiFetch("/api/tasks/9").catch((e: unknown) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 404, code: "not_found" });
    expect(describeError(err)).toBe("That task does not exist. Pick another task.");
  });

  it("gives a plain message when the error body is not an ErrorBody", async () => {
    respond(502, "<html>bad gateway</html>");
    const err = await apiFetch("/api/x").catch((e: unknown) => e);

    expect(err).toMatchObject({ status: 502, code: "internal" });
    expect(describeError(err)).not.toContain("html");
  });

  it("reports an unreachable server in plain language", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))));
    const err = await apiFetch("/api/x").catch((e: unknown) => e);

    expect(err).toMatchObject({ status: 0, code: "unavailable" });
    expect(describeError(err)).toMatch(/can't reach/i);
  });

  it("describes unknown errors without leaking their text", () => {
    expect(describeError(new Error("ECONNRESET at C:\\secret"))).toBe("Something went wrong. Try again.");
  });
});
