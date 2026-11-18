// Always start Playwright with a new data directory and no ambient Yggdrasil
// host/account configuration. Use Node for portability to native Windows.
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";

const require = createRequire(new URL("../web/package.json", import.meta.url));
const cli = require.resolve("@playwright/test/cli");
const testEnv = Object.fromEntries(
  Object.entries(process.env).filter(([key]) => !key.startsWith("YGG_")),
);
testEnv.YGG_E2E_DATA_DIR = mkdtempSync(join(tmpdir(), "ygg-e2e-"));
if (process.env.YGG_E2E_PORT) testEnv.YGG_E2E_PORT = process.env.YGG_E2E_PORT;
// One server and one database serve every spec (the specs share one tracked
// workspace), so run them one after another unless the caller says otherwise.
const args = process.argv.slice(2);
if (!args.some((a) => a === "--workers" || a.startsWith("--workers="))) args.push("--workers=1");
const result = spawnSync(process.execPath, [cli, "test", ...args], {
  cwd: new URL("../web", import.meta.url),
  env: testEnv,
  stdio: "inherit",
});
if (result.error) throw result.error;
process.exitCode = result.status ?? 1;
