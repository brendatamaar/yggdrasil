import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { stubApi, type Handler } from "../../test/fakeApi";
import { ProposalsPanel } from "./ProposalsPanel";

const hashA = "a".repeat(64);
const hashB = "b".repeat(64);

const proposal = (over: Record<string, unknown> = {}) => ({
  id: "p1",
  changeSetId: "cs1",
  taskId: "t1",
  repoId: "r1",
  repoName: "shop-api",
  kind: "commit",
  status: "pending",
  source: "agent",
  fromAgent: true,
  summary: "Fix the order total",
  lines: ['git commit -m "Fix the order total"', "2 files changed"],
  actionHash: hashA,
  attempt: 0,
  createdAt: "2026-10-27T10:00:00Z",
  ...over,
});

function setup(list: () => unknown[], extra?: Handler) {
  const api = stubApi((c) => {
    const custom = extra?.(c);
    if (custom) return custom;
    if (c.path === "/api/proposals?task=t1") return { body: { proposals: list() } };
    return undefined;
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ProposalsPanel taskId="t1" />
    </QueryClientProvider>,
  );
  return api;
}

describe("ProposalsPanel", () => {
  it("shows what will run and confirms with the hash it rendered", async () => {
    const api = setup(
      () => [proposal()],
      (c) => (c.path === "/api/proposals/p1/confirm" ? { body: proposal({ status: "executed" }) } : undefined),
    );
    expect(await screen.findByText("Fix the order total")).toBeTruthy();
    expect(screen.getByText(/belong together/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Review and confirm" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("2 files changed")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() => {
      expect(api.calls.find((c) => c.path === "/api/proposals/p1/confirm")?.body).toEqual({ hash: hashA });
    });
  });

  it("on 409 says it changed and only sends the new hash after Review again", async () => {
    let current = hashA;
    const api = setup(
      () => [proposal({ actionHash: current })],
      (c) => {
        if (c.path === "/api/proposals/p1/confirm") {
          current = hashB;
          return {
            status: 409,
            body: { code: "conflict", message: "What you were shown changed. Reload the page and review the proposal again." },
          };
        }
        return undefined;
      },
    );
    fireEvent.click(await screen.findByRole("button", { name: "Review and confirm" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
    expect(await within(dialog).findByText(/changed while you were looking/)).toBeTruthy();
    expect(within(dialog).queryByRole("button", { name: "Confirm" })).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "Review again" }));
    const confirm = await within(screen.getByRole("alertdialog")).findByRole("button", { name: "Confirm" });
    fireEvent.click(confirm);
    await waitFor(() => {
      const posts = api.calls.filter((c) => c.path === "/api/proposals/p1/confirm");
      expect(posts.map((c) => c.body)).toEqual([{ hash: hashA }, { hash: hashB }]);
    });
  });

  it("rejects, and offers Retry for an interrupted proposal", async () => {
    const api = setup(() => [
      proposal(),
      proposal({ id: "p2", changeSetId: undefined, kind: "push", status: "failed", failKind: "interrupted", retryable: true, message: "" }),
    ]);
    fireEvent.click(await screen.findByRole("button", { name: "Reject" }));
    await waitFor(() => {
      expect(api.calls.some((c) => c.method === "POST" && c.path === "/api/proposals/p1/reject")).toBe(true);
    });
    expect(screen.getByText("Interrupted")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => {
      expect(api.calls.some((c) => c.path === "/api/proposals/p2/retry")).toBe(true);
    });
  });

  it("renders the agent's summary as text, not markup", async () => {
    setup(() => [proposal({ summary: "<img src=x onerror=alert(1)>" })]);
    expect(await screen.findByText("<img src=x onerror=alert(1)>")).toBeTruthy();
    expect(document.querySelector("img")).toBeNull();
  });
});
