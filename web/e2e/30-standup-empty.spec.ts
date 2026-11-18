import { expect, test } from "@playwright/test";
import { fakeAgentStarts } from "./support/guards";
import { REPOS, seedWorkspace, type Workspace } from "./support/workspace";

// AU-02/UI-03 standup and UI-07 empty states. Empty views are success states, distinct
// from loading and from errors. The zero-project and empty-project views need a server
// with no data, so they answer from mocked API responses; everything else is the real binary.

let ws: Workspace;
test.beforeAll(async ({ request }) => {
  ws = await seedWorkspace(request);
});

const counts = { all: 0, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 0, problems: 0 };

test.describe("standup (real binary)", () => {
  test("a range reads commits, copy works, and Polish only starts after its own click", async ({ page, context }) => {
    await context.grantPermissions(["clipboard-read", "clipboard-write"]);
    const started = await fakeAgentStarts(page);
    const reads: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/standup")) reads.push(`${r.method()} ${new URL(r.url()).pathname}`);
    });
    await page.goto("/");
    const widget = page.getByRole("heading", { name: "Standup" });
    await expect(widget).toBeVisible();
    // Nothing is read until Refresh, and nothing starts an agent.
    await expect(page.getByText("Nothing is read until you do.")).toBeVisible();
    expect(reads).toHaveLength(0);

    await page.getByRole("button", { name: "Refresh" }).click();
    const summary = page.getByLabel("Standup summary");
    await expect(summary).toBeVisible();
    await expect(summary).toContainText("first commit");
    expect(reads.every((r) => r.startsWith("GET "))).toBe(true);
    expect(started).toHaveLength(0);

    await page.getByRole("button", { name: "Copy" }).click();
    await expect(page.getByText("Copied.")).toBeVisible();
    const clip = await page.evaluate(() => navigator.clipboard.readText());
    expect(clip).toContain("first commit");
    expect(started).toHaveLength(0);

    // Polish is explicit: one click, one request, and only now.
    await page.getByRole("button", { name: "Polish with agent" }).click();
    await expect.poll(() => started.filter((s) => s.path.endsWith("/standup/polish"))).toHaveLength(1);
  });

  test("a range with nothing in it is the empty state, not an error", async ({ page }) => {
    await page.goto("/");
    await page.getByLabel("First day").fill("2020-01-01");
    await page.getByLabel("Last day").fill("2020-01-02");
    await page.getByRole("button", { name: "Refresh" }).click();
    await expect(page.getByText("Nothing happened in this range.")).toBeVisible();
    await expect(page.getByRole("button", { name: "Copy" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Polish with agent" })).toBeDisabled();
  });

  test("a range over the limit is refused in the form, before any request", async ({ page }) => {
    const reads: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/standup")) reads.push(r.url());
    });
    await page.goto("/");
    await page.getByLabel("First day").fill("2020-01-01");
    await page.getByLabel("Last day").fill("2020-12-31");
    await page.getByRole("button", { name: "Refresh" }).click();
    await expect(page.getByRole("alert")).toBeVisible();
    expect(reads).toHaveLength(0);
  });
});

test.describe("empty states are not errors", () => {
  test("zero projects", async ({ page }) => {
    await page.route(/\/api\/repos(\?.*)?$/, (route) => route.fulfill({ json: { projects: [], counts } }));
    await page.goto("/");
    await expect(page.getByText("No projects yet. Add a project folder to get started.").first()).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
  });

  test("one project with no repos", async ({ page }) => {
    const project = { id: "prj_0000000000000000", name: "acme-empty", path: "/tmp/acme-empty", repos: [], counts };
    await page.route(/\/api\/repos(\?.*)?$/, (route) => route.fulfill({ json: { projects: [project], counts } }));
    await page.goto("/");
    await expect(page.getByText("This project has no repos yet.")).toBeVisible();
  });

  test("a failed read is an error with its own words, never the empty message", async ({ page }) => {
    await page.route(/\/api\/repos(\?.*)?$/, (route) =>
      route.fulfill({ status: 500, json: { code: "internal", message: "Something went wrong. Try again.", hint: "Reload." } }),
    );
    await page.goto("/");
    await expect(page.getByRole("alert").first()).toBeVisible();
    await expect(page.getByText("No projects yet.")).toHaveCount(0);
  });

  test("no builds", async ({ page }) => {
    await page.route(/\/api\/builds(\?.*)?$/, (route) => route.fulfill({ json: { builds: [] } }));
    await page.goto("/builds");
    await expect(page.getByText("No builds yet. Start one with New build.")).toBeVisible();
  });

  test("no merge requests and no remote on a repo with none", async ({ page }) => {
    await page.goto(`/repos/${ws.ids.local}`);
    await expect(page.getByRole("heading", { name: REPOS.local })).toBeVisible();
    await expect(page.getByText("Nothing to act on in this repo right now.")).toBeVisible();
    // The host area explains itself and is not a red error.
    await expect(page.getByRole("region", { name: "Host context" })).toBeVisible();
  });
});
