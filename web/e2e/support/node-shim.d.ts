/// <reference lib="dom" />
// The e2e files run in Node and in the page (page.evaluate), so the DOM lib is added above. But the tooling tsconfig carries no @types/node
// (package.json is foundation-owned). These are the few calls the fixtures use,
// declared by hand; widen them here when a spec needs another call.
declare module "node:child_process" {
  export function execFileSync(
    file: string,
    args: readonly string[],
    options?: { cwd?: string; env?: Record<string, string | undefined>; encoding?: "utf8"; stdio?: "pipe" | "ignore" },
  ): string;
}
declare module "node:fs" {
  export function mkdirSync(path: string, options?: { recursive?: boolean }): string | undefined;
  export function mkdtempSync(prefix: string): string;
  export function writeFileSync(path: string, data: string): void;
  export function renameSync(from: string, to: string): void;
  export function existsSync(path: string): boolean;
  export function readFileSync(path: string, encoding: "utf8"): string;
}
declare module "node:os" {
  export function tmpdir(): string;
}
declare module "node:path" {
  export function join(...parts: string[]): string;
}
declare const process: { env: Record<string, string | undefined> };
