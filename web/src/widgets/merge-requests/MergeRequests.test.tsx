import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { InboxItemDTO, InboxResponse } from "../../api/gen/api.gen";
import { renderWidget } from "../../test/renderWidget";
import { stubApi } from "../../test/fakeApi";
import { MergeRequests } from "./MergeRequests";

const item = (n: number, role: string, url?: string, state = "open"): InboxItemDTO => ({
  mr: {
    number: n,
    title: `MR ${String(n)}`,
    url: url ?? `https://git.acme.dev/acme-shop/shop-api/-/merge_requests/${String(n)}`,
    author: "ada",
    source: "fix",
    target: "main",
    state,
    updatedAt: "2026-01-01T10:00:00Z",
  },
  instance: "git.acme.dev",
  repo: "acme-shop/shop-api",
  role,
});
const reply = (items: InboxItemDTO[]): InboxResponse => ({ items, instances: [] });

describe("Merge requests widget", () => {
  it("shows a loading state first", async () => {
    stubApi(() => ({ body: reply([]) }));
    await renderWidget(MergeRequests);
    expect(screen.getByText("Loading…")).toBeTruthy();
  });

  it("lists review requests first and skips merged ones", async () => {
    stubApi(() => ({
      body: reply([item(1, "authored"), item(2, "review_requested"), item(3, "assigned", undefined, "merged")]),
    }));
    await renderWidget(MergeRequests);
    const links = await screen.findAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(["MR 2", "MR 1"]);
    expect(links[0]?.getAttribute("href")).toContain("merge_requests/2");
    expect(links[0]?.getAttribute("rel")).toContain("noopener");
  });

  it("renders an unsafe URL as plain text, not a link", async () => {
    stubApi(() => ({ body: reply([item(1, "authored", "javascript:alert(1)")]) }));
    await renderWidget(MergeRequests);
    expect(await screen.findByText("MR 1")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "MR 1" })).toBeNull();
  });

  it("explains an empty inbox and links to the inbox page", async () => {
    stubApi(() => ({ body: reply([]) }));
    await renderWidget(MergeRequests);
    expect(await screen.findByText(/No open merge requests/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Open the inbox" }).getAttribute("href")).toBe("/mrs");
  });

  it("shows an error", async () => {
    stubApi(() => ({ status: 500, body: { code: "internal", message: "Something went wrong.", hint: "Try again." } }));
    await renderWidget(MergeRequests);
    expect((await screen.findByRole("alert")).textContent).toContain("Something went wrong.");
  });
});
