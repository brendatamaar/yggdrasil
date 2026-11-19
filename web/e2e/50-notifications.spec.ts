import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { seedWorkspace, until, type Workspace } from "./support/workspace";

// UI-06, NR-style notifications through the built binary and the mock (demo) agent:
// the browser permission is asked for only from a click, a desktop notice shows only while the
// tab is in the background, the in-app notice is always there, and a reload shows no second
// toast. The browser's Notification API is replaced by a recording fake (a real permission
// prompt cannot be driven headlessly); the real behaviour is on the Windows checklist.

let ws: Workspace;
test.beforeAll(async ({ request }) => {
  ws = await seedWorkspace(request);
});

interface Toast {
  title: string;
  body: string;
}
declare global {
  interface Window {
    __toasts: Toast[];
    __asks: number;
    __vis: "visible" | "hidden";
    __setVisibility: (v: "visible" | "hidden") => void;
  }
}

/** A fake Notification and a controllable page visibility; the permission survives a reload. */
async function installFakes(page: Page, initial: "default" | "granted" | "denied"): Promise<void> {
  await page.addInitScript((start) => {
    const key = "e2e-notification-permission";
    if (sessionStorage.getItem(key) === null) sessionStorage.setItem(key, start);
    window.__toasts = [];
    window.__asks = 0;
    window.__vis = "visible";
    // A constructor function (the app calls `new Notification(title, options)`).
    const fake = function (title: string, options?: { body?: string }) {
      window.__toasts.push({ title, body: options?.body ?? "" });
    };
    Object.defineProperty(fake, "permission", { get: () => sessionStorage.getItem(key) ?? "default" });
    Object.defineProperty(fake, "requestPermission", {
      value: () => {
        window.__asks += 1;
        sessionStorage.setItem(key, "granted");
        return Promise.resolve("granted");
      },
    });
    Object.defineProperty(window, "Notification", { value: fake, configurable: true });
    Object.defineProperty(document, "visibilityState", { get: () => window.__vis, configurable: true });
    window.__setVisibility = (v) => {
      window.__vis = v;
      document.dispatchEvent(new Event("visibilitychange"));
    };
  }, initial);
}

/** One finished Ask task of the demo agent; returns once its notice is in the server history. */
async function finishTask(request: APIRequestContext, title: string): Promise<void> {
  const res = await request.post("/api/tasks", {
    data: {
      projectId: ws.projectId,
      repoIds: [ws.ids.local],
      title,
      // Code-like prompt text: it must never reach a notice.
      prompt: "Look at src/payments/secret_keys.go and the token in it.",
      mode: "ask",
      agent: "mock",
    },
  });
  expect(res.ok(), await res.text()).toBe(true);
  await until(`a notice for ${title}`, async () => {
    const n = await request.get("/api/notifications");
    const body = (await n.json()) as { notices: { taskTitle: string }[] };
    return body.notices.some((x) => x.taskTitle === title) ? true : undefined;
  });
}

test("the permission is asked for only from a click, never on load", async ({ page }) => {
  await installFakes(page, "default");
  await page.goto("/");
  await page.getByRole("button", { name: "Notifications" }).click();
  await expect(page.getByRole("button", { name: "Turn on desktop notices" })).toBeVisible();
  expect(await page.evaluate(() => window.__asks)).toBe(0);
  await page.reload();
  expect(await page.evaluate(() => window.__asks)).toBe(0);
  await page.getByRole("button", { name: "Notifications" }).click();
  await page.getByRole("button", { name: "Turn on desktop notices" }).click();
  await expect.poll(() => page.evaluate(() => window.__asks)).toBe(1);
  await expect(page.getByRole("button", { name: "Turn on desktop notices" })).toHaveCount(0);
});

test("a denied browser keeps the in-app list and says so", async ({ page }) => {
  await installFakes(page, "denied");
  await page.goto("/");
  await page.getByRole("button", { name: "Notifications" }).click();
  await expect(page.getByText("Desktop notices are blocked in your browser settings. Notices still show here.")).toBeVisible();
  expect(await page.evaluate(() => window.__asks)).toBe(0);
});

test("foreground: in-app notice only; background: one desktop notice with only the task name and status; reload: no repeat", async ({ page, request }) => {
  await installFakes(page, "granted");
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Notifications" })).toBeVisible();

  // Foreground: the in-app notice appears, no desktop notice.
  await finishTask(request, "Foreground check");
  await expect(page.getByRole("button", { name: /Notifications, \d+ unread/ })).toBeVisible();
  await page.getByRole("button", { name: /Notifications/ }).click();
  await expect(page.getByRole("list", { name: "Notifications" }).getByText("Foreground check")).toBeVisible();
  await page.keyboard.press("Escape");
  expect(await page.evaluate(() => window.__toasts)).toHaveLength(0);

  // Background: the same kind of event now also reaches the desktop, once.
  await page.evaluate(() => {
    window.__setVisibility("hidden");
  });
  await finishTask(request, "Background check");
  await expect.poll(() => page.evaluate(() => window.__toasts.length)).toBe(1);
  const [toast] = await page.evaluate(() => window.__toasts);
  expect(toast?.title).toBe("Background check");
  expect(`${toast?.title ?? ""} ${toast?.body ?? ""}`).not.toMatch(/secret_keys|payments|token|\.go/);
  // The in-app notice is there as well.
  await page.evaluate(() => {
    window.__setVisibility("visible");
  });
  await page.getByRole("button", { name: /Notifications/ }).click();
  await expect(page.getByRole("list", { name: "Notifications" }).getByText("Background check")).toBeVisible();
  await page.keyboard.press("Escape");

  // Reload in the background: the history is replayed, and none of it becomes a desktop notice.
  await page.reload();
  await page.evaluate(() => {
    window.__setVisibility("hidden");
  });
  await page.waitForTimeout(1500);
  expect(await page.evaluate(() => window.__toasts)).toHaveLength(0);
});
