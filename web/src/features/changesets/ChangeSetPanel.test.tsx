import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { ChangeSetPanel } from "./ChangeSetPanel";

const member = (id: string, repoId: string, over: Record<string, unknown> = {}) => ({
  proposalId: id,
  repoId,
  kind: "merge_request",
  status: "pending",
  hash: `hash-${id}-0123456789`,
  ...over,
});

const cs = (over: Record<string, unknown> = {}) => ({
  id: "cs1",
  taskId: "t1",
  status: "open",
  state: "open",
  repos: [
    { repoId: "r1", name: "shop-core" },
    { repoId: "r2", name: "shop-checkout" },
  ],
  order: ["r1", "r2"],
  suggestedOrder: ["r1", "r2"],
  members: [member("p1", "r1"), member("p2", "r2")],
  links: [],
  run: { state: "idle" },
  warnings: [],
  ...over,
});

const worktree = (over: Record<string, unknown> = {}) => ({
  id: "w1",
  repoId: "r1",
  repoName: "shop-core",
  taskId: "t1",
  name: "w",
  branch: "ygg/cart-fix",
  baseCommit: "abc",
  status: "active",
  createdAt: "2026-10-30T10:00:00Z",
  ...over,
});

interface Setup {
  cs?: Record<string, unknown>;
  csReply?: Reply;
  worktrees?: unknown[];
  extra?: (c: Call) => Reply | undefined;
}

function setup(o: Setup = {}) {
  const api = stubApi((c) => {
    const custom = o.extra?.(c);
    if (custom) return custom;
    if (c.path === "/api/tasks/t1/changeset") return o.csReply ?? { body: o.cs ?? cs() };
    if (c.path === "/api/tasks/t1/worktrees") return { body: { worktrees: o.worktrees ?? [worktree()] } };
    return undefined;
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ChangeSetPanel taskId="t1" />
    </QueryClientProvider>,
  );
  return api;
}

const posts = (api: ReturnType<typeof setup>, path: string) => api.calls.filter((c) => c.method === "POST" && c.path === path);

describe("change set panel states", () => {
  it("groups proposals by repo with their status", async () => {
    setup({ cs: cs({ members: [member("p1", "r1"), member("p2", "r2", { status: "executed" })] }) });
    const g1 = await screen.findByRole("group", { name: "Proposals in shop-core" });
    expect(within(g1).getByText("Waiting for you")).toBeTruthy();
    expect(within(screen.getByRole("group", { name: "Proposals in shop-checkout" })).getByText("Done")).toBeTruthy();
  });

  it("says so on 501 and shows the reason of other errors", async () => {
    setup({ csReply: { status: 501, body: { code: "not_ready", message: "x" } } });
    expect(await screen.findByText("Change sets aren't available yet.")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows a server error in plain words", async () => {
    setup({ csReply: { status: 409, body: { code: "conflict", message: "This change set moved.", hint: "Reload." } } });
    expect((await screen.findByRole("alert", {}, { timeout: 4000 })).textContent).toContain("This change set moved.");
  });

  it("lists linked MRs in order with the backwards note", async () => {
    setup({
      cs: cs({
        links: [
          { repoId: "r1", url: "https://git.example.test/shop-core/-/merge_requests/1" },
          { repoId: "r2", url: "https://git.example.test/shop-checkout/-/merge_requests/4" },
        ],
      }),
    });
    const list = await screen.findByRole("list", { name: "Linked merge requests" });
    const links = within(list).getAllByRole("link");
    expect(links.map((l) => l.getAttribute("href"))).toEqual([
      "https://git.example.test/shop-core/-/merge_requests/1",
      "https://git.example.test/shop-checkout/-/merge_requests/4",
    ]);
    expect(screen.getByText(/links back to the ones created before/)).toBeTruthy();
  });

  it("closes the change set", async () => {
    const api = setup();
    fireEvent.click(await screen.findByRole("button", { name: "Close change set" }));
    await waitFor(() => {
      expect(posts(api, "/api/changesets/cs1/close")).toHaveLength(1);
    });
  });

  it("shows where a halted run stopped and why", async () => {
    setup({ cs: cs({ run: { state: "halted", at: "p2", reason: "the push was rejected" } }) });
    expect((await screen.findByRole("status")).textContent).toContain("Stopped at shop-checkout: the push was rejected");
  });

  it("shows progress and Stop while a run is in progress, and Stop posts", async () => {
    const api = setup({ cs: cs({ state: "confirming", confirming: { scope: { kind: "all" }, current: "p1", remaining: 2 } }) });
    expect((await screen.findByRole("status")).textContent).toContain("2 left");
    expect(screen.getByRole<HTMLButtonElement>("button", { name: /Confirm all/ }).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => {
      expect(posts(api, "/api/changesets/cs1/stop")).toHaveLength(1);
    });
  });
});

describe("merge order", () => {
  it("moves a repo down with the keyboard-operable buttons and sends the permutation", async () => {
    const api = setup({ extra: (c) => (c.method === "PUT" ? { body: cs({ order: ["r2", "r1"] }) } : undefined) });
    const down = await screen.findByRole("button", { name: "Move shop-core down" });
    down.focus();
    expect(document.activeElement).toBe(down);
    fireEvent.click(down);
    await waitFor(() => {
      expect(api.calls).toContainEqual(expect.objectContaining({ method: "PUT", path: "/api/changesets/cs1/order", body: { order: ["r2", "r1"] } }));
    });
  });

  it("disables the edges and, while confirming, everything", async () => {
    setup({ cs: cs({ state: "confirming", confirming: { scope: { kind: "all" }, current: "p1", remaining: 1 } }) });
    expect((await screen.findByRole<HTMLButtonElement>("button", { name: "Move shop-core down" })).disabled).toBe(true);
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Move shop-core up" }).disabled).toBe(true);
  });

  it("offers the suggested order when it differs", async () => {
    const api = setup({
      cs: cs({ order: ["r2", "r1"], suggestedOrder: ["r1", "r2"] }),
      extra: (c) => (c.method === "PUT" ? { body: cs() } : undefined),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Use suggested order" }));
    await waitFor(() => {
      expect(api.calls).toContainEqual(expect.objectContaining({ method: "PUT", body: { order: ["r1", "r2"] } }));
    });
  });

  it("shows dependency warnings from the server", async () => {
    setup({ cs: cs({ warnings: [{ code: "dependent_before_dependency", message: "shop-checkout depends on shop-core." }] }) });
    expect((await screen.findByRole("list", { name: "Order warnings" })).textContent).toContain("shop-checkout depends on shop-core.");
  });
});

describe("confirm all", () => {
  const warning = { code: "dependent_before_dependency", dependent: "r2", dependency: "r1", message: "shop-checkout is merged before shop-core." };

  it("shows exactly the proposals and hashes, and sends the hashes seen", async () => {
    const api = setup({
      extra: (c) => {
        if (c.path === "/api/changesets/cs1/preflight") return { body: { warnings: [] } };
        if (c.path === "/api/changesets/cs1/confirm") return { body: cs({ state: "confirming" }) };
        return undefined;
      },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Confirm all (2)" }));
    const list = await screen.findByRole("list", { name: "Proposals to confirm" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(list.textContent).toContain("hash-p1-0123".slice(0, 12));
    const confirm = await screen.findByRole<HTMLButtonElement>("button", { name: "Confirm" });
    await waitFor(() => {
      expect(confirm.disabled).toBe(false);
    });
    expect(posts(api, "/api/changesets/cs1/confirm")).toHaveLength(0);
    fireEvent.click(confirm);
    await waitFor(() => {
      expect(posts(api, "/api/changesets/cs1/confirm")[0]?.body).toEqual({
        scope: { kind: "all" },
        seen: { p1: "hash-p1-0123456789", p2: "hash-p2-0123456789" },
      });
    });
    await waitFor(() => {
      expect(screen.queryByRole("alertdialog")).toBeNull();
    });
  });

  it("will not send until the warning is acknowledged, then sends the ack", async () => {
    const api = setup({
      extra: (c) => {
        if (c.path === "/api/changesets/cs1/preflight") return { body: { warnings: [warning] } };
        if (c.path === "/api/changesets/cs1/confirm") return { body: cs() };
        return undefined;
      },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Confirm all (2)" }));
    expect(await screen.findByText("shop-checkout is merged before shop-core.")).toBeTruthy();
    const confirm = screen.getByRole<HTMLButtonElement>("button", { name: "Confirm" });
    expect(confirm.disabled).toBe(true);
    fireEvent.click(confirm);
    expect(posts(api, "/api/changesets/cs1/confirm")).toHaveLength(0);
    fireEvent.click(screen.getByRole("checkbox"));
    expect(confirm.disabled).toBe(false);
    fireEvent.click(confirm);
    await waitFor(() => {
      expect((posts(api, "/api/changesets/cs1/confirm")[0]?.body as { ack: string[] }).ack).toEqual(["dependent_before_dependency"]);
    });
  });

  it("per-repo confirm covers only that repo", async () => {
    const api = setup({
      extra: (c) => {
        if (c.path === "/api/changesets/cs1/preflight") return { body: { warnings: [] } };
        if (c.path === "/api/changesets/cs1/confirm") return { body: cs() };
        return undefined;
      },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Confirm shop-checkout" }));
    const confirm = await screen.findByRole<HTMLButtonElement>("button", { name: "Confirm" });
    await waitFor(() => {
      expect(confirm.disabled).toBe(false);
    });
    fireEvent.click(confirm);
    await waitFor(() => {
      expect(posts(api, "/api/changesets/cs1/confirm")[0]?.body).toEqual({
        scope: { kind: "repo", repoId: "r2" },
        seen: { p2: "hash-p2-0123456789" },
      });
    });
  });

  it("shows the server's reason when it refuses and keeps the dialog open", async () => {
    setup({
      extra: (c) => {
        if (c.path === "/api/changesets/cs1/preflight") return { body: { warnings: [] } };
        if (c.path === "/api/changesets/cs1/confirm")
          return { status: 409, body: { code: "busy", message: "A run is already going.", hint: "Wait for it or stop it." } };
        return undefined;
      },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Confirm all (2)" }));
    const confirm = await screen.findByRole<HTMLButtonElement>("button", { name: "Confirm" });
    await waitFor(() => {
      expect(confirm.disabled).toBe(false);
    });
    fireEvent.click(confirm);
    expect((await screen.findByRole("alert")).textContent).toContain("A run is already going.");
    expect(screen.getByRole("alertdialog")).toBeTruthy();
  });

  it("says not available yet on 501 and Cancel sends nothing", async () => {
    setup({
      extra: (c) => {
        if (c.path === "/api/changesets/cs1/preflight") return { body: { warnings: [] } };
        if (c.path === "/api/changesets/cs1/confirm") return { status: 501, body: { code: "not_ready", message: "x" } };
        return undefined;
      },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Confirm all (2)" }));
    const confirm = await screen.findByRole<HTMLButtonElement>("button", { name: "Confirm" });
    await waitFor(() => {
      expect(confirm.disabled).toBe(false);
    });
    fireEvent.click(confirm);
    expect((await screen.findByRole("alert")).textContent).toContain("isn't available yet");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});

describe("landing dialogs", () => {
  it("merge creates a merge_local proposal request and never confirms", async () => {
    const api = setup({ extra: (c) => (c.path === "/api/worktrees/w1/merge-local" ? { status: 201, body: { id: "px" } } : undefined) });
    fireEvent.click(await screen.findByRole("button", { name: "Merge shop-core into main checkout" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("nothing is pushed");
    expect(dialog.textContent).toContain("ygg/cart-fix");
    fireEvent.change(within(dialog).getByRole("textbox"), { target: { value: "Land cart fix" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create proposal" }));
    await waitFor(() => {
      expect(posts(api, "/api/worktrees/w1/merge-local")[0]?.body).toEqual({ title: "Land cart fix" });
    });
    expect(await screen.findByText(/Proposal created/)).toBeTruthy();
    expect(api.calls.some((c) => c.path.includes("/confirm"))).toBe(false);
  });

  it("remove creates a remove_worktree proposal request and says the branch is kept", async () => {
    const api = setup({
      worktrees: [worktree({ status: "merged" })],
      extra: (c) => (c.path === "/api/worktrees/w1/remove" ? { status: 201, body: { id: "py" } } : undefined),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Remove shop-core worktree" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("The branch is kept");
    expect(dialog.textContent).toContain("merged");
    fireEvent.click(within(dialog).getByRole("button", { name: "Create proposal" }));
    await waitFor(() => {
      expect(posts(api, "/api/worktrees/w1/remove")).toHaveLength(1);
    });
    expect(api.calls.some((c) => c.path.includes("/confirm"))).toBe(false);
  });

  it("shows a refusal in the dialog and lets the person cancel", async () => {
    setup({
      extra: (c) =>
        c.path === "/api/worktrees/w1/remove" ? { status: 409, body: { code: "conflict", message: "The task is still running.", hint: "Stop it first." } } : undefined,
    });
    fireEvent.click(await screen.findByRole("button", { name: "Remove shop-core worktree" }));
    fireEvent.click(await screen.findByRole("button", { name: "Create proposal" }));
    expect((await screen.findByRole("alert")).textContent).toContain("The task is still running.");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("Escape closes the dialog without a request", async () => {
    const api = setup();
    fireEvent.click(await screen.findByRole("button", { name: "Remove shop-core worktree" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.keyDown(dialog, { key: "Escape" });
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });
});
