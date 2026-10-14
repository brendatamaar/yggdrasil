// Checks `pnpm licenses list --json` output against an allowlist.
// Usage: node scripts/check-npm-licenses.mjs <licenses.json> <comma-separated SPDX IDs>
// Handles simple SPDX expressions: "A OR B" passes if either is allowed,
// "A AND B" only if both are.
import { readFileSync } from "node:fs";

const [file, allowedArg] = process.argv.slice(2);
if (!file || !allowedArg) {
  console.error("usage: check-npm-licenses.mjs <licenses.json> <allowed,ids>");
  process.exit(2);
}

const allowed = new Set(allowedArg.split(",").map((s) => s.trim()));
const byLicense = JSON.parse(readFileSync(file, "utf8"));

function isAllowed(expr) {
  const e = expr.replace(/[()]/g, "").trim();
  if (e.includes(" OR ")) return e.split(" OR ").some(isAllowed);
  if (e.includes(" AND ")) return e.split(" AND ").every(isAllowed);
  return allowed.has(e);
}

const bad = [];
let count = 0;
for (const [license, pkgs] of Object.entries(byLicense)) {
  for (const pkg of pkgs) {
    count++;
    if (!isAllowed(license)) bad.push(`${pkg.name}@${pkg.versions.join(",")}: ${license}`);
  }
}

if (bad.length > 0) {
  console.error(`npm license check failed (${bad.length} of ${count} packages):`);
  for (const line of bad) console.error(`  ${line}`);
  process.exit(1);
}
console.log(`npm license check passed (${count} packages)`);
