import { expect, test } from "@playwright/test";

// The one scenario of the foundation: the built binary starts and answers its
// health endpoint with the loopback-only contract. It needs no browser binary
// (the request fixture), so it proves the runner, the config and the Taskfile
// wiring end to end. The browser scenarios arrive with the end-to-end task.
test("the built ygg answers /api/health", async ({ request }) => {
  const res = await request.get("/api/health");
  expect(res.ok()).toBe(true);
  const body = (await res.json()) as { status: string; version: string; writeModes: boolean };
  expect(body.status).toBe("ok");
  expect(body.writeModes).toBe(false);
  expect(body.version).not.toBe("");
});
