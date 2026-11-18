import type { Page, Request } from "@playwright/test";

// The default agent of a fresh ygg is the first available one, which on a
// developer machine can be a real agent CLI. The browser specs therefore never
// let a request that would start an agent reach the server: they record it and
// answer with a fake. A spec that presses such a button on purpose asserts on
// the recorded request; every other spec asserts the list stays empty.

const AGENT_STARTING = [
  /\/api\/actions\/execute$/,
  /\/api\/tasks$/,
  /\/api\/tasks\/[^/]+\/(messages|start)$/,
  /\/api\/projects\/[^/]+\/standup\/polish$/,
];

export interface Recorded {
  method: string;
  path: string;
  body: string;
}

/** Records and fakes every agent-starting POST of the page. Returns the live list. */
export async function fakeAgentStarts(page: Page, opts: { passExecute?: boolean } = {}): Promise<Recorded[]> {
  const seen: Recorded[] = [];
  await page.route(
    (url) => AGENT_STARTING.some((re) => re.test(url.pathname)),
    async (route) => {
      const req: Request = route.request();
      if (req.method() !== "POST") {
        await route.continue();
        return;
      }
      const path = new URL(req.url()).pathname;
      seen.push({ method: req.method(), path, body: req.postData() ?? "" });
      // The pull and ignore flows submit through the action endpoint without an agent: they may
      // reach the server (the body is recorded so a spec can check it names no agent).
      if (opts.passExecute && path === "/api/actions/execute") {
        await route.continue();
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ state: "succeeded", message: "Fake: no agent was started in this test." }),
      });
    },
  );
  return seen;
}

/** Sets the stored theme before the app loads ("harbour" is dark, "daylight" is light). */
export async function useTheme(page: Page, theme: "harbour" | "daylight"): Promise<void> {
  await page.addInitScript((t) => {
    localStorage.setItem("ygg-theme", JSON.stringify({ state: { theme: t }, version: 2 }));
  }, theme);
}
