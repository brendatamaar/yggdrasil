import { expect, test, type APIRequestContext } from "@playwright/test";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fakeAgentStarts } from "./support/guards";
import { git, REPOS, seedWorkspace, until, type Workspace } from "./support/workspace";

// Confirmation flows (AC-04, WS-09, invariant 2): the dialog shows the exact
// confirmation code the server holds, nothing changes on disk before Confirm,
// and a file edited after the proposal makes the confirmation refuse.

let ws: Workspace;
test.beforeAll(async ({ request }) => {
  ws = await seedWorkspace(request);
});

interface Proposal {
  id: string;
  kind: string;
  status: string;
  actionHash: string;
  repoId?: string;
}
async function proposalsOf(request: APIRequestContext, repoId: string, kind: string): Promise<Proposal[]> {
  const res = await request.get("/api/proposals?status=pending");
  expect(res.ok()).toBe(true);
  const all = (await res.json()) as { proposals: Proposal[] } | Proposal[];
  const list = Array.isArray(all) ? all : all.proposals;
  return list.filter((p) => p.repoId === repoId && p.kind === kind);
}

test("pull: the dialog shows the exact code, nothing is pulled before Confirm, then the repo is fast-forwarded", async ({ page, request }) => {
  const sent = await fakeAgentStarts(page, { passExecute: true });
  const headBefore = git(ws.dirs.pull, "rev-parse", "HEAD").trim();
  await page.goto("/");
  await page.getByRole("list", { name: `Actions for ${REPOS.pull}` }).getByRole("button", { name: "Pull and check" }).click();
  const action = page.getByRole("dialog", { name: "Pull and check" });
  await expect(action.getByText("Nothing is pulled until you confirm it.")).toBeVisible();
  await action.getByRole("button", { name: "Create pull proposal" }).click();

  await expect(action.getByRole("status").first()).toBeVisible();
  await action.getByRole("button", { name: "Review and confirm" }).click();
  const confirm = page.getByRole("alertdialog", { name: "Pull (fast-forward only)" });
  await expect(confirm).toBeVisible();

  // The code on screen is the code the server holds for this proposal.
  const [proposal] = await proposalsOf(request, ws.ids.pull, "pull_ff");
  expect(proposal).toBeDefined();
  await expect(confirm.getByText(proposal?.actionHash ?? "missing")).toBeVisible();
  expect(sent.every((r) => !r.body.includes('"agent"'))).toBe(true);

  // Looking is not doing: the repo has not moved.
  expect(git(ws.dirs.pull, "rev-parse", "HEAD").trim()).toBe(headBefore);
  await confirm.getByRole("button", { name: "Cancel" }).click();
  expect(git(ws.dirs.pull, "rev-parse", "HEAD").trim()).toBe(headBefore);

  await action.getByRole("button", { name: "Review and confirm" }).click();
  await page.getByRole("alertdialog", { name: "Pull (fast-forward only)" }).getByRole("button", { name: "Confirm" }).click();
  await until("the pull to land", () => Promise.resolve(git(ws.dirs.pull, "rev-parse", "HEAD").trim() !== headBefore ? true : undefined));
  expect(existsSync(join(ws.dirs.pull, "pricing.md"))).toBe(true);
});

test("ignore: exact lines, no write before Confirm, a file edited meanwhile is refused, CRLF and user entries survive", async ({ page, request }) => {
  const ignore = join(ws.dirs.noisy, ".gitignore");
  await page.goto(`/repos/${ws.ids.noisy}`);
  const suggestions = page.getByRole("region", { name: "Ignore suggestions" });
  await suggestions.getByRole("checkbox").first().check();
  await suggestions.getByRole("button", { name: "Create ignore proposal" }).click();
  await suggestions.getByRole("button", { name: "Review and confirm" }).click();
  let confirm = page.getByRole("alertdialog", { name: "Add entries to .gitignore" });
  await expect(confirm.getByRole("list", { name: "What will run" })).toContainText("dist");
  const [first] = await proposalsOf(request, ws.ids.noisy, "ignore_entries");
  await expect(confirm.getByText(first?.actionHash ?? "missing")).toBeVisible();
  expect(existsSync(ignore)).toBe(false);
  await confirm.getByRole("button", { name: "Cancel" }).click();
  expect(existsSync(ignore)).toBe(false);

  // Someone edits .gitignore after the proposal was made: the confirmation must refuse.
  writeFileSync(ignore, "coverage/\r\n");
  await suggestions.getByRole("button", { name: "Review and confirm" }).click();
  confirm = page.getByRole("alertdialog", { name: "Add entries to .gitignore" });
  await confirm.getByRole("button", { name: "Confirm" }).click();
  const review = page.getByRole("region", { name: "Review" });
  await expect(review.getByText("Stale")).toBeVisible();
  await expect(review.getByText(".gitignore changed since this was proposed, so nothing was written.")).toBeVisible();
  expect(readFileSync(ignore, "utf8")).toBe("coverage/\r\n");

  // Propose again against the file as it is now: the confirmation lands, keeps the user's
  // entry and keeps CRLF line endings.
  await page.reload();
  const again = page.getByRole("region", { name: "Ignore suggestions" });
  await again.getByRole("checkbox").first().check();
  await again.getByRole("button", { name: "Create ignore proposal" }).click();
  await again.getByRole("button", { name: "Review and confirm" }).click();
  await page.getByRole("alertdialog", { name: "Add entries to .gitignore" }).getByRole("button", { name: "Confirm" }).click();
  await until(".gitignore to change", () => Promise.resolve(readFileSync(ignore, "utf8") !== "coverage/\r\n" ? true : undefined));
  const text = readFileSync(ignore, "utf8");
  expect(text.startsWith("coverage/\r\n")).toBe(true);
  expect(text).toContain("dist/");
  expect(text.replace(/\r\n/g, "")).not.toContain("\n");
});
