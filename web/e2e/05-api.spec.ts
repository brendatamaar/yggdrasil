import { expect, test } from "@playwright/test";
import { REPOS, seedWorkspace, type RepoKey, type Workspace } from "./support/workspace";

// Request-only checks of the Phase 6 endpoints of the built binary (no browser needed).

let ws: Workspace;
test.beforeAll(async ({ request }) => {
  ws = await seedWorkspace(request);
});

test("the workspace is tracked and each fixture repo answers an action catalogue", async ({ request }) => {
  const res = await request.get("/api/repos");
  const body = (await res.json()) as { projects: { name: string; repos: { name: string }[] }[] };
  const names = body.projects.flatMap((p) => p.repos.map((r) => r.name));
  for (const k of Object.keys(REPOS) as RepoKey[]) expect(names).toContain(REPOS[k]);
  for (const k of Object.keys(REPOS) as RepoKey[]) {
    const c = await request.get(`/api/repos/${ws.ids[k]}/actions`);
    expect(c.status(), REPOS[k]).toBe(200);
  }
});

test("an unknown repo has no catalogue", async ({ request }) => {
  const res = await request.get("/api/repos/rep_0000000000000000/actions");
  expect(res.status()).toBeGreaterThanOrEqual(400);
  expect(res.status()).toBeLessThan(500);
});

test("the notifications endpoint answers with the documented shape", async ({ request }) => {
  const res = await request.get("/api/notifications");
  expect(res.ok()).toBe(true);
  const body = (await res.json()) as { gap: boolean; newest: number; notices: unknown[] };
  expect(typeof body.gap).toBe("boolean");
  expect(Array.isArray(body.notices)).toBe(true);
});

test("standup answers from local data and a range over the limit is refused", async ({ request }) => {
  const day = new Date().toISOString().slice(0, 10);
  const ok = await request.get(`/api/projects/${ws.projectId}/standup?from=${day}T00:00:00Z&to=${day}T23:59:59Z`);
  expect(ok.ok(), await ok.text()).toBe(true);
  const bad = await request.get(`/api/projects/${ws.projectId}/standup?from=2020-01-01T00:00:00Z&to=2020-12-31T00:00:00Z`);
  expect(bad.status()).toBeGreaterThanOrEqual(400);
  expect(bad.status()).toBeLessThan(500);
});

test("a mutation without a JSON body is refused", async ({ request }) => {
  const bad = await request.post("/api/actions/execute", { data: "not json", headers: { "content-type": "text/plain" } });
  expect(bad.status()).toBeGreaterThanOrEqual(400);
  expect(bad.status()).toBeLessThan(500);
});
