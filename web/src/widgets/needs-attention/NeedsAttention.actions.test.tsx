import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import * as gen from "../../api/gen/api.gen";
import type { ActionCatalogueResponse, ActionOfferingDTO, BuildDTO, RepoStatusDTO } from "../../api/gen/api.gen";
import { stubApi, type Call } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { NeedsAttention } from "./NeedsAttention";

const counts = { all: 3, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 0, problems: 0 };
const repo = (id: string, name: string, over: Partial<RepoStatusDTO> = {}): RepoStatusDTO => ({
  id,
  projectId: "p1",
  name,
  relPath: name,
  path: `/work/acme-shop/${name}`,
  trusted: false,
  refreshing: false,
  view: "fresh",
  flags: [],
  branch: "main",
  detached: false,
  upstream: "origin/main",
  hasUpstream: true,
  ahead: 0,
  behind: 0,
  staged: 0,
  unstaged: 0,
  untracked: 0,
  conflicted: 0,
  failedPipeline: false,
  ...over,
});
const reposBody = (list: RepoStatusDTO[]) => ({
  counts,
  projects: [{ id: "p1", name: "acme-shop", path: "/work/acme-shop", counts, repos: list }],
});
const failedBuild = (id: string, repoId: string): BuildDTO => ({
  id,
  status: "failed",
  target: "repos",
  actor: "user",
  queuedAt: "2026-01-01T10:00:00Z",
  steps: [{ index: 0, repoId, name: "build", status: "failed", argv: ["go"], attempt: 1, logTruncated: false }],
  artifactsCollected: false,
  artifacts: [],
});

// One repo state per generated attention reason. A reason added to the generated types without
// a row here fails the "every reason" test below.
const FIXTURE: Record<string, { repo: Partial<RepoStatusDTO>; failedBuild?: boolean }> = {
  [gen.ReasonUnreadable]: { repo: { view: "problem", problem: "missing", problemMessage: "The folder is missing." } },
  [gen.ReasonConflicts]: { repo: { conflicted: 2 } },
  [gen.ReasonDiverged]: { repo: { flags: ["diverged"] } },
  [gen.ReasonFailedCI]: { repo: { failedPipeline: true } },
  [gen.ReasonFailedBuild]: { repo: {}, failedBuild: true },
  [gen.ReasonBehind]: { repo: { behind: 3 } },
  [gen.ReasonDirty]: { repo: { unstaged: 1 } },
};

const allReasons = (): string[] =>
  Object.entries(gen)
    .filter(([k, v]) => k.startsWith("Reason") && typeof v === "string")
    .map(([, v]) => v);

function handler(list: RepoStatusDTO[], builds: BuildDTO[], catalogue?: (repoId: string) => ActionOfferingDTO[]) {
  return (c: Call) => {
    if (c.path.startsWith("/api/repos?")) return { body: reposBody(list) };
    if (c.path.startsWith("/api/builds")) return { body: { builds } };
    const m = /^\/api\/repos\/([^/]+)\/actions$/.exec(c.path);
    if (m && catalogue) return { body: { repoId: m[1] ?? "", offerings: catalogue(m[1] ?? "") } satisfies ActionCatalogueResponse };
    return undefined;
  };
}

const actionList = (name: string) => screen.findByRole("list", { name: `Actions for ${name}` });

describe("Needs attention actions (UI-07, AC-06)", () => {
  it("knows every generated attention reason", () => {
    expect(allReasons().sort()).toEqual(Object.keys(FIXTURE).sort());
  });

  for (const reason of allReasons()) {
    it(`reason ${reason}: shows at least one action even when the catalogue cannot be read`, async () => {
      const f = FIXTURE[reason];
      if (!f) throw new Error(`no fixture for reason ${reason}`);
      stubApi(handler([repo("r1", "shop-api", f.repo)], f.failedBuild ? [failedBuild("b1", "r1")] : []));
      await renderWidget(NeedsAttention);
      const list = await actionList("shop-api");
      const buttons = within(list)
        .getAllByRole("button")
        .filter((b) => b.closest("li")?.getAttribute("data-reason") === reason);
      expect(buttons.length).toBeGreaterThan(0);
      for (const b of buttons) expect(b.textContent.trim()).not.toBe("");
    });
  }

  it("an unreadable repo offers a diagnosis that says it waits for the folder", async () => {
    stubApi(handler([repo("r1", "shop-api", FIXTURE[gen.ReasonUnreadable]?.repo)], []));
    await renderWidget(NeedsAttention);
    const list = await actionList("shop-api");
    expect(within(list).getByRole("button", { name: "Diagnose repo" })).toBeTruthy();
    expect(within(list).getByText("waits for folder")).toBeTruthy();
  });

  it("uses the server's catalogue when it has offerings, adding a fallback only for reasons it left out", async () => {
    stubApi(
      handler([repo("r1", "shop-api", { failedPipeline: true, behind: 1 })], [], () => [
        { kind: "diagnose_pipeline", reason: "failed_ci", title: "Diagnose with agent", context: "available", sourceId: "run-7" },
        { kind: "fix_pipeline", reason: "failed_ci", title: "Fix in worktree", mode: "code", context: "available", sourceId: "run-7" },
      ]),
    );
    await renderWidget(NeedsAttention);
    const list = await actionList("shop-api");
    await waitFor(() => {
      expect(within(list).getByRole("button", { name: "Fix in worktree" })).toBeTruthy();
    });
    // behind had no catalogue entry: the safe fallback stands in.
    expect(within(list).getByRole("button", { name: "Pull and check" })).toBeTruthy();
  });

  it("opens the shared preview for the picked action and sends nothing else", async () => {
    const api = stubApi((c) => {
      const base = handler([repo("r1", "shop-api", { behind: 2 })], [])(c);
      if (base) return base;
      if (c.path === "/api/actions/preview") {
        const req = c.body as { requestId: string };
        return {
          body: {
            requestId: req.requestId,
            digest: "d1",
            kind: "pull_and_check",
            source: "shop-api main, 2 behind origin/main",
            contextText: "Fast-forward only.",
            context: "available",
            expectedHead: "0123456789abcdef",
          },
        };
      }
      return undefined;
    });
    await renderWidget(NeedsAttention);
    const list = await actionList("shop-api");
    fireEvent.click(within(list).getByRole("button", { name: "Pull and check" }));
    await screen.findByText("shop-api main, 2 behind origin/main");
    // Only the preview was requested; no execute.
    expect(api.calls.filter((c) => c.path === "/api/actions/execute")).toHaveLength(0);
    expect(api.calls.filter((c) => c.path === "/api/actions/preview")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Create pull proposal" })).toBeTruthy();
  });

  it("shows NoProjects and EmptyProject as their own states, and an error as an error", async () => {
    stubApi((c) => (c.path.startsWith("/api/repos?") ? { body: { counts, projects: [] } } : undefined));
    const a = await renderWidget(NeedsAttention);
    await waitFor(() => {
      expect(a.container.querySelector('[data-empty="NoProjects"]')).not.toBeNull();
    });
    a.unmount();

    stubApi((c) =>
      c.path.startsWith("/api/repos?")
        ? { body: { counts, projects: [{ id: "p1", name: "acme-shop", path: "/w", counts, repos: [] }] } }
        : undefined,
    );
    const b = await renderWidget(NeedsAttention);
    await waitFor(() => {
      expect(b.container.querySelector('[data-empty="EmptyProject"]')).not.toBeNull();
    });
    b.unmount();

    stubApi((c) =>
      c.path.startsWith("/api/repos?") ? { status: 500, body: { code: "internal", message: "The server hit a problem.", hint: "Try again." } } : undefined,
    );
    const e = await renderWidget(NeedsAttention);
    await screen.findByRole("alert");
    expect(e.container.querySelector("[data-empty]")).toBeNull();
    expect(screen.queryByText("Nothing needs you right now.")).toBeNull();
  });
});
