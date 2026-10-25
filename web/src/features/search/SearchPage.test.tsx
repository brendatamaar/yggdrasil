import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import type { SearchResponse } from "../../api/gen/api.gen";
import { createAppRouter } from "../../router";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";

const match = (over: Partial<SearchResponse["matches"][number]>) => ({
  repo: "r1",
  repoName: "shop-api",
  path: "src/Cart.java",
  line: 12,
  col: 3,
  text: "  total := cart.Total()",
  editorUrl: "vscode://file/work/shop-api/src/Cart.java:12:3",
  ...over,
});

const repos = {
  counts: {},
  projects: [{ id: "p1", name: "acme-shop", path: "x", counts: {}, repos: [{ id: "r1", name: "shop-api" }, { id: "r2", name: "shop-web" }] }],
};

function renderSearch(onSearch: (c: Call) => Reply) {
  const api = stubApi((c) => {
    if (c.path.startsWith("/api/repos")) return { body: repos };
    if (c.path === "/api/search") return onSearch(c);
    return undefined;
  });
  FakeSocket.instances = [];
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/search"] }));
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
  return api;
}

async function submit(text: string) {
  const box = await screen.findByRole("textbox", { name: "Search pattern" });
  fireEvent.change(box, { target: { value: text } });
  fireEvent.submit(box);
}

describe("search page", () => {
  it("focuses the box and sends the query with options", async () => {
    const { calls } = renderSearch(() => ({ body: { matches: [], problems: [], truncated: false } }));
    const box = await screen.findByRole("textbox", { name: "Search pattern" });
    await waitFor(() => {
      expect(document.activeElement).toBe(box);
    });
    fireEvent.click(screen.getByLabelText("Regex"));
    fireEvent.click(screen.getByLabelText("Match case"));
    fireEvent.click(await screen.findByLabelText("shop-web"));
    fireEvent.change(screen.getByRole("textbox", { name: "File patterns" }), { target: { value: "*.java, src/**/*.ts" } });
    await submit("Total\\(");
    await waitFor(() => {
      expect(calls.find((c) => c.path === "/api/search")?.body).toEqual({
        pattern: "Total\\(",
        mode: "regex",
        case: true,
        repos: ["r2"],
        paths: ["*.java", "src/**/*.ts"],
      });
    });
  });

  it("shows loading, then results grouped by repo and file with editor links", async () => {
    renderSearch(() => ({
      body: { matches: [match({}), match({ line: 40, editorUrl: "javascript:alert(1)" }), match({ repo: "r2", repoName: "shop-web", path: "a.ts" })], problems: [], truncated: false },
    }));
    await submit("total");
    const api = await screen.findByRole("region", { name: "shop-api" });
    expect(within(api).getByText("src/Cart.java")).toBeTruthy();
    const link = within(api).getByRole("link", { name: "Open line 12 in editor" });
    expect(link.getAttribute("href")).toBe("vscode://file/work/shop-api/src/Cart.java:12:3");
    expect(within(api).queryByRole("link", { name: "Open line 40 in editor" })).toBeNull();
    expect(within(api).getByText("40")).toBeTruthy();
    expect(screen.getByRole("region", { name: "shop-web" })).toBeTruthy();
    expect(within(api).getAllByText("total := cart.Total()")).toHaveLength(2);
  });

  it("shows the empty state", async () => {
    renderSearch(() => ({ body: { matches: [], problems: [], truncated: false } }));
    await submit("nothing");
    expect(await screen.findByText("No matches.")).toBeTruthy();
  });

  it("shows problems in plain language and the truncated notice", async () => {
    renderSearch(() => ({
      body: {
        matches: [match({})],
        problems: [{ repo: "r2", repoName: "shop-web", kind: "timed_out", message: "Try a narrower search." }],
        truncated: true,
      },
    }));
    await submit("total");
    expect(await screen.findByText(/took too long here/)).toBeTruthy();
    expect(screen.getByText(/Try a narrower search/)).toBeTruthy();
    expect(screen.getByText(/Too many matches/)).toBeTruthy();
  });

  it("shows an error", async () => {
    renderSearch(() => ({ status: 400, body: { code: "bad_request", message: "That pattern is not valid.", hint: "Check the pattern." } }));
    await submit("(");
    expect((await screen.findByRole("alert")).textContent).toMatch(/not valid/);
  });

  it("renders match text as plain text, never HTML", async () => {
    renderSearch(() => ({ body: { matches: [match({ text: '<img src=x onerror="alert(1)">' })], problems: [], truncated: false } }));
    await submit("img");
    const api = await screen.findByRole("region", { name: "shop-api" });
    expect(within(api).getByText('<img src=x onerror="alert(1)">')).toBeTruthy();
    expect(api.querySelector("img")).toBeNull();
  });

  it("moves through results with the arrow keys", async () => {
    renderSearch(() => ({ body: { matches: [match({}), match({ line: 20, col: 1 })], problems: [], truncated: false } }));
    await submit("total");
    const box = screen.getByRole("textbox", { name: "Search pattern" });
    const rows = await screen.findAllByRole("listitem");
    box.focus();
    fireEvent.keyDown(box, { key: "ArrowDown" });
    expect(document.activeElement).toBe(rows[0]);
    fireEvent.keyDown(rows[0] as HTMLElement, { key: "ArrowDown" });
    expect(document.activeElement).toBe(rows[1]);
    fireEvent.keyDown(rows[1] as HTMLElement, { key: "ArrowUp" });
    expect(document.activeElement).toBe(rows[0]);
    fireEvent.keyDown(rows[0] as HTMLElement, { key: "ArrowUp" });
    expect(document.activeElement).toBe(box);
  });
});
