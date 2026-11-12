import { defineConfig, devices } from "@playwright/test";

// Browser end-to-end tests (task test:e2e). They run against the BUILT binary
// (task build writes ../bin/ygg), not the Vite dev server, so what they see is
// what ships. The server starts in a throwaway data folder with the session
// cookie switched off (YGG_INSECURE_NO_SESSION, refused when write modes are
// on) and never opens a browser. The scenarios live in e2e/; the Phase 6
// scenarios (needs-attention matrix, notifications, new repo, standup) are
// added by the end-to-end task and use fake hosts and the mock agent only: no
// real agent CLI, model API or git host.
// Node's `process`, declared by hand so the config type-checks without adding
// @types/node (the tsconfig for tooling files has no Node types).
declare const process: { env: Record<string, string | undefined>; platform: string };

const port = Number(process.env["YGG_E2E_PORT"] ?? 4749);
const dataDir = process.env["YGG_E2E_DATA_DIR"];
if (!dataDir) throw new Error("Set YGG_E2E_DATA_DIR to an isolated temporary directory; use task test:e2e.");
if (!Number.isInteger(port) || port < 1024 || port > 65535) {
  throw new Error("YGG_E2E_PORT must be an integer between 1024 and 65535.");
}

export default defineConfig({
  testDir: "e2e",
  fullyParallel: true,
  forbidOnly: Boolean(process.env["CI"]),
  retries: process.env["CI"] ? 1 : 0,
  reporter: process.env["CI"] ? [["github"], ["list"]] : "list",
  outputDir: "test-results",
  use: {
    baseURL: `http://127.0.0.1:${String(port)}`,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: process.platform === "win32" ? "..\\bin\\ygg.exe" : "../bin/ygg",
    url: `http://127.0.0.1:${String(port)}/api/health`,
    reuseExistingServer: false,
    timeout: 60_000,
    env: {
      YGG_ADDR: `127.0.0.1:${String(port)}`,
      YGG_NO_BROWSER: "1",
      YGG_INSECURE_NO_SESSION: "1",
      YGG_DATA_DIR: dataDir,
      YGG_WRITE_MODES: "0",
    },
  },
});
