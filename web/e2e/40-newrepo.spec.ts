import { expect, test } from "@playwright/test";
import { existsSync } from "node:fs";
import { join } from "node:path";
import { fakeAgentStarts } from "./support/guards";
import { PROJECT_NAME, seedWorkspace, until, type Workspace } from "./support/workspace";

// NR-01 to NR-04: a new repo inside a tracked project. Reserving the name creates no
// folder; the init is a confirmation; publishing needs a host and a token.
//
// LIMIT: the publish step against a fake host is NOT covered here. The built binary can be
// pointed at a host with YGG_HOST_n, but a publish pushes over the host's smart-HTTP git
// protocol and needs a git server behind the fake (git http-backend). That is covered at the Go
// level with fakes (cmd/ygg phase6_newrepos_test.go). This spec covers what runs without a
// host: preview, reserve, the init confirmation, and the no-credentials publish guidance.

let ws: Workspace;
test.beforeAll(async ({ request }) => {
  ws = await seedWorkspace(request);
});

const LEAF = "shop-checkout";

test("new repo: preview, reserve, init only after the confirmation, publish explains the missing token", async ({ page }) => {
  const started = await fakeAgentStarts(page);
  const folder = join(ws.root, PROJECT_NAME, LEAF);
  expect(existsSync(folder)).toBe(false);

  await page.goto("/projects");
  await page.getByRole("button", { name: `Actions for project ${PROJECT_NAME}` }).click();
  await page.getByRole("menuitem", { name: "New repo..." }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Folder name").fill(LEAF);
  await dialog.getByRole("button", { name: "Preview" }).click();

  // The preview names exactly what will happen, with a code; still nothing on disk.
  await expect(dialog.getByRole("list", { name: "What will happen" })).toBeVisible();
  await expect(dialog.getByText("Confirmation code:")).toBeVisible();
  expect(existsSync(folder)).toBe(false);

  await dialog.getByRole("button", { name: "Reserve and review" }).click();
  // Reserving is still not creating: the init is its own confirmation.
  await expect(dialog.getByRole("button", { name: "Review and confirm" })).toBeVisible();
  expect(existsSync(folder)).toBe(false);

  await dialog.getByRole("button", { name: "Review and confirm" }).click();
  const confirm = page.getByRole("alertdialog", { name: "Set up a new repo" });
  await expect(confirm.getByText("Confirmation code:")).toBeVisible();
  expect(existsSync(folder)).toBe(false);
  await confirm.getByRole("button", { name: "Confirm" }).click();
  await until("the new repo folder", () => Promise.resolve(existsSync(join(folder, ".git")) ? true : undefined));

  // No git host or token is configured in this suite: publish says what to add and creates nothing.
  await expect(dialog.getByText(/token|host|remote/i).first()).toBeVisible();
  expect(started).toHaveLength(0);
});

test("a folder name that leaves the project is refused in the form", async ({ page }) => {
  await page.goto("/projects");
  await page.getByRole("button", { name: `Actions for project ${PROJECT_NAME}` }).click();
  await page.getByRole("menuitem", { name: "New repo..." }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Folder name").fill("../outside");
  await expect(dialog.getByRole("alert")).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Preview" })).toBeDisabled();
});
