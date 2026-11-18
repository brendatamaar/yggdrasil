import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, renameSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { APIRequestContext } from "@playwright/test";

// A fictional workspace for the browser tests: the project "acme-e2e" with one
// repo per Needs-attention reason, built with the git CLI in a temporary folder
// (never a real repo, never a real host: every remote is a local bare repo).
// The ygg under test runs in a throwaway data folder, so tracking these repos
// only touches that folder's database.

export const PROJECT_NAME = "acme-e2e";

/** The fixture repos; each one is meant to show exactly the reason in its comment. */
export const REPOS = {
  dirty: "shop-dirty", // uncommitted changes
  behind: "shop-behind", // behind its upstream
  failed: "shop-failed", // its newest local build failed
  broken: "shop-broken", // the folder cannot be read
  diverged: "shop-diverged", // ahead and behind
  conflicts: "shop-conflicts", // a merge stopped on a conflict
  pull: "shop-pull", // behind, pulled by the confirmation test
  noisy: "shop-noisy", // many generated untracked files, ignored by the confirmation test
  local: "shop-local", // clean, no remote at all
} as const;
export type RepoKey = keyof typeof REPOS;

export interface Workspace {
  root: string;
  projectId: string;
  /** Repo ids by key. */
  ids: Record<RepoKey, string>;
  /** Repo folders by key. */
  dirs: Record<RepoKey, string>;
}

const GIT_ENV = {
  GIT_AUTHOR_NAME: "Ada Lindqvist",
  GIT_AUTHOR_EMAIL: "ada.lindqvist@example.com",
  GIT_COMMITTER_NAME: "Ada Lindqvist",
  GIT_COMMITTER_EMAIL: "ada.lindqvist@example.com",
  GIT_TERMINAL_PROMPT: "0",
};

/** Runs git in cwd and returns stdout. Throws with git's message when it fails. */
export function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", ["-c", "core.autocrlf=false", "-c", "commit.gpgsign=false", ...args], {
    cwd,
    encoding: "utf8",
    env: { ...process.env, ...GIT_ENV },
    stdio: "pipe",
  });
}

function write(dir: string, name: string, text: string): void {
  writeFileSync(join(dir, name), text);
}

/** A repo with one commit on main and, when remote is true, a local bare origin it was pushed to. */
export function makeRepo(root: string, name: string, remote: boolean): string {
  const dir = join(root, PROJECT_NAME, name);
  mkdirSync(dir, { recursive: true });
  git(dir, "init", "-q", "-b", "main");
  write(dir, "README.md", `# ${name}\n\nFictional repo of ${PROJECT_NAME}.\n`);
  git(dir, "add", "-A");
  git(dir, "commit", "-q", "-m", "chore: first commit");
  if (remote) {
    const bare = join(root, "remotes", `${name}.git`);
    mkdirSync(bare, { recursive: true });
    git(bare, "init", "-q", "--bare", "-b", "main");
    git(dir, "remote", "add", "origin", bare);
    git(dir, "push", "-q", "-u", "origin", "main");
  }
  return dir;
}

/** Pushes one more commit to the repo's origin from a second clone, then fetches it into the repo. */
export function advanceRemote(root: string, name: string, dir: string, file: string): void {
  const other = join(root, "clones", name);
  mkdirSync(join(root, "clones"), { recursive: true });
  git(root, "clone", "-q", join(root, "remotes", `${name}.git`), other);
  write(other, file, `change from another developer in ${name}\n`);
  git(other, "add", "-A");
  git(other, "commit", "-q", "-m", `feat: add ${file}`);
  git(other, "push", "-q", "origin", "main");
  git(dir, "fetch", "-q", "origin");
}

async function api<T>(request: APIRequestContext, method: "GET" | "POST", path: string, body?: unknown): Promise<T> {
  const res = await request.fetch(path, { method, ...(body === undefined ? {} : { data: body }) });
  if (!res.ok()) throw new Error(`${method} ${path} answered ${String(res.status())}: ${await res.text()}`);
  return (await res.json()) as T;
}

/** Waits until check passes, polling the server; throws the last problem when time runs out. */
export async function until<T>(what: string, fn: () => Promise<T | undefined>, ms = 20_000): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v !== undefined) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 250));
  }
}

interface RepoRow {
  id: string;
  name: string;
  view: string;
  behind: number;
  staged: number;
  unstaged: number;
  untracked: number;
  flags: string[];
}
interface ReposResponse {
  projects: { id: string; name: string; repos: RepoRow[] }[];
}

/** One refresh of every tracked repo, then the rows of one project. */
export async function readRepos(request: APIRequestContext, projectId: string): Promise<RepoRow[]> {
  await api(request, "POST", "/api/repos/refresh", {});
  const res = await api<ReposResponse>(request, "GET", "/api/repos");
  return res.projects.find((p) => p.id === projectId)?.repos ?? [];
}

let cached: Workspace | undefined;

/**
 * Builds the workspace once per worker and tracks it as the project "acme-e2e"; later calls
 * (and other workers) find the same project through the API. Builds one failed local build
 * for the "failed" repo and makes the "broken" repo unreadable.
 */
export async function seedWorkspace(request: APIRequestContext): Promise<Workspace> {
  if (cached) return cached;
  const existing = await api<ReposResponse>(request, "GET", "/api/repos");
  const found = existing.projects.find((p) => p.name === PROJECT_NAME);
  if (found) {
    // Another worker built it: rebuild the map from the server, the folders from the repo paths.
    const rows = await api<{ projects: { id: string; name: string; repos: (RepoRow & { path: string })[] }[] }>(request, "GET", "/api/repos");
    const proj = rows.projects.find((p) => p.name === PROJECT_NAME);
    if (!proj) throw new Error("project vanished");
    const ids = {} as Record<RepoKey, string>;
    const dirs = {} as Record<RepoKey, string>;
    for (const k of Object.keys(REPOS) as RepoKey[]) {
      const r = proj.repos.find((x) => x.name === REPOS[k]);
      if (!r) throw new Error(`repo ${REPOS[k]} is not tracked`);
      ids[k] = r.id;
      dirs[k] = r.path;
    }
    cached = { root: "", projectId: proj.id, ids, dirs };
    return cached;
  }

  const root = mkdtempSync(join(tmpdir(), "ygg-e2e-ws-"));
  const dirs = {} as Record<RepoKey, string>;
  for (const k of Object.keys(REPOS) as RepoKey[]) dirs[k] = makeRepo(root, REPOS[k], k !== "local");

  // dirty: an edit and an untracked file.
  write(dirs.dirty, "README.md", "# shop-dirty\n\nEdited and not committed.\n");
  write(dirs.dirty, "notes.txt", "scratch\n");
  // behind (twice, one is for the pull confirmation).
  advanceRemote(root, REPOS.behind, dirs.behind, "catalog.md");
  advanceRemote(root, REPOS.pull, dirs.pull, "pricing.md");
  // diverged: one commit on each side.
  advanceRemote(root, REPOS.diverged, dirs.diverged, "remote-only.md");
  write(dirs.diverged, "local-only.md", "only here\n");
  git(dirs.diverged, "add", "-A");
  git(dirs.diverged, "commit", "-q", "-m", "feat: local-only change");
  // conflicts: the same line changed on two branches, merge stopped.
  write(dirs.conflicts, "README.md", "# shop-conflicts\n\nmain side\n");
  git(dirs.conflicts, "commit", "-q", "-a", "-m", "docs: main side");
  git(dirs.conflicts, "checkout", "-q", "-b", "other", "HEAD~1");
  write(dirs.conflicts, "README.md", "# shop-conflicts\n\nother side\n");
  git(dirs.conflicts, "commit", "-q", "-a", "-m", "docs: other side");
  git(dirs.conflicts, "checkout", "-q", "main");
  try {
    git(dirs.conflicts, "merge", "other");
  } catch {
    // The merge stops on a conflict: that is the state the fixture wants.
  }
  // noisy: 150 generated files under one folder, more than the noise threshold of 100.
  mkdirSync(join(dirs.noisy, "build-output"), { recursive: true });
  for (let i = 0; i < 150; i++) write(join(dirs.noisy, "build-output"), `chunk-${String(i)}.js`, `// generated ${String(i)}\n`);

  const picks = (Object.keys(REPOS) as RepoKey[]).map((k) => ({ relPath: REPOS[k], name: REPOS[k] }));
  const tracked = await api<{ projectId: string; added: string[] }>(request, "POST", "/api/projects", {
    path: join(root, PROJECT_NAME),
    name: PROJECT_NAME,
    picks,
  });
  const rows = await until("every fixture repo to be read", async () => {
    const r = await readRepos(request, tracked.projectId);
    return r.length === picks.length && r.every((x) => x.view !== "pending") ? r : undefined;
  });
  const ids = {} as Record<RepoKey, string>;
  for (const k of Object.keys(REPOS) as RepoKey[]) {
    const row = rows.find((r) => r.name === REPOS[k]);
    if (!row) throw new Error(`repo ${REPOS[k]} was not tracked`);
    ids[k] = row.id;
  }

  // failed: a local build whose one step fails (git rejects the flag), recorded in build history.
  const build = await api<{ buildId: string }>(request, "POST", "/api/builds", {
    target: "main",
    steps: [{ repoId: ids.failed, name: "verify", argv: ["git", "status", "--no-such-flag"] }],
  });
  await until("the failing build to finish", async () => {
    const b = await api<{ status: string }>(request, "GET", `/api/builds/${build.buildId}`);
    return b.status === "failed" ? b : undefined;
  });

  // broken: the folder stops being a repo after tracking (renamed, never deleted).
  renameSync(join(dirs.broken, ".git"), join(dirs.broken, ".git-off"));
  await until("the broken repo to be reported unreadable", async () => {
    const r = await readRepos(request, tracked.projectId);
    return r.find((x) => x.id === ids.broken)?.view === "problem" ? true : undefined;
  });

  cached = { root, projectId: tracked.projectId, ids, dirs };
  return cached;
}
