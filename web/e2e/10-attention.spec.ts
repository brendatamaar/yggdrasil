import { expect, test, type Page } from "@playwright/test";
import { fakeAgentStarts } from "./support/guards";
import { REPOS, seedWorkspace, type RepoKey, type Workspace } from "./support/workspace";

// E6 (every Needs-attention item has an agent action) through the built binary:
// a seeded fictional workspace, one repo per reason, the shared action dialog
// opened from the widget row, the repo detail page and the command palette.
// The dialog is opened and cancelled: pressing its start button would run the
// default agent, so no spec here does (see support/guards.ts).

/** The reason each fixture repo shows and the action that answers it. */
const EXPECT: { key: RepoKey; reason: string; action: string }[] = [
  { key: "broken", reason: "unreadable", action: "Diagnose repo" },
  { key: "conflicts", reason: "conflicts", action: "Diagnose repo" },
  { key: "diverged", reason: "diverged", action: "Diagnose repo" },
  { key: "failed", reason: "failed_build", action: "Diagnose build" },
  { key: "behind", reason: "behind", action: "Pull and check" },
  { key: "dirty", reason: "dirty", action: "Propose commits" },
];

let ws: Workspace;
test.beforeAll(async ({ request }) => {
  ws = await seedWorkspace(request);
});

async function openHome(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Needs attention" })).toBeVisible();
}

test("the catalogue offers an action for each of the seven reasons", async ({ request }) => {
  const byReason = new Map<string, string[]>();
  for (const key of Object.keys(REPOS) as RepoKey[]) {
    const res = await request.get(`/api/repos/${ws.ids[key]}/actions`);
    expect(res.ok()).toBe(true);
    const body = (await res.json()) as { offerings: { kind: string; reason: string }[] };
    for (const o of body.offerings) byReason.set(o.reason, [...(byReason.get(o.reason) ?? []), o.kind]);
  }
  for (const reason of ["unreadable", "conflicts", "diverged", "failed_build", "behind", "dirty"]) {
    expect(byReason.get(reason) ?? [], `an offering for ${reason}`).not.toHaveLength(0);
  }
  // A failed pipeline needs a recorded host answer; no host runs here, so its catalogue entry is
  // covered by the Go tests (TestPhase6NeedsAttentionActions) and by the mocked UI case below.
});

test("every attention row shows an action, and a clean repo shows none", async ({ page }) => {
  await openHome(page);
  for (const e of EXPECT) {
    const list = page.getByRole("list", { name: `Actions for ${REPOS[e.key]}` });
    await expect(list.getByRole("button", { name: e.action }), `${REPOS[e.key]} offers ${e.action}`).toBeVisible();
  }
  await expect(page.getByRole("list", { name: `Actions for ${REPOS.local}` })).toHaveCount(0);
});

test("the unreadable repo says its draft waits for the folder", async ({ page }) => {
  await openHome(page);
  const list = page.getByRole("list", { name: `Actions for ${REPOS.broken}` });
  await expect(list.getByText("waits for folder")).toBeVisible();
});

for (const e of EXPECT.filter((x) => x.action !== "Pull and check")) {
  test(`${e.reason}: the widget row opens the preview and nothing starts`, async ({ page }) => {
    const started = await fakeAgentStarts(page);
    await openHome(page);
    await page.getByRole("list", { name: `Actions for ${REPOS[e.key]}` }).getByRole("button", { name: e.action }).click();
    const dialog = page.getByRole("dialog", { name: e.action });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText(REPOS[e.key], { exact: true })).toBeVisible();
    await expect(dialog.getByText("Preview first. Nothing starts until you press the button.")).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Cancel" })).toBeVisible();
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toBeHidden();
    expect(started).toHaveLength(0);
  });
}

test("the repo detail page offers the same action in the same dialog", async ({ page }) => {
  const started = await fakeAgentStarts(page);
  await page.goto(`/repos/${ws.ids.conflicts}`);
  const section = page.getByRole("region", { name: "Agent actions" });
  await section.getByRole("button", { name: "Diagnose repo" }).click();
  const dialog = page.getByRole("dialog", { name: "Diagnose repo" });
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  expect(started).toHaveLength(0);
});

test("the command palette finds the action and opens the preview, not the start", async ({ page }) => {
  const started = await fakeAgentStarts(page);
  await openHome(page);
  await page.getByRole("list", { name: `Actions for ${REPOS.dirty}` }).waitFor();
  await page.keyboard.press("Control+k");
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();
  await palette.getByRole("combobox").fill(`Propose commits: ${REPOS.dirty}`);
  await page.keyboard.press("Enter");
  await expect(page.getByRole("dialog", { name: "Propose commits" })).toBeVisible();
  expect(started).toHaveLength(0);
});

test("a failed pipeline reason shows its action (mocked host answer)", async ({ page }) => {
  // No git host runs in this suite, so the pipeline flag comes from a mocked repo list.
  const rows = await page.request.get("/api/repos");
  const data = (await rows.json()) as { projects: { repos: { id: string; failedPipeline: boolean }[] }[] };
  await page.route(/\/api\/repos(\?.*)?$/, async (route) => {
    for (const p of data.projects) for (const r of p.repos) if (r.id === ws.ids.local) r.failedPipeline = true;
    await route.fulfill({ json: data });
  });
  await openHome(page);
  const list = page.getByRole("list", { name: `Actions for ${REPOS.local}` });
  await expect(list.getByRole("button", { name: "Diagnose repo" })).toBeVisible();
  await expect(list.getByRole("button", { name: "Diagnose repo" })).toHaveAttribute("type", "button");
});
