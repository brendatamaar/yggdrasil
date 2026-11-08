import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DevServerDTO } from "../../api/gen/api.gen";
import { stubApi } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { RunningServices } from "./RunningServices";

const server = (over: Partial<DevServerDTO>): DevServerDTO => ({
  id: "s1",
  repoId: "r1",
  repoName: "shop-web",
  command: "npm run dev",
  status: "running",
  ports: [{ port: 41002, announced: true, outsideBlock: false }],
  ...over,
});

describe("Running services widget", () => {
  it("lists servers with repo, state and a port link, orphans first", async () => {
    stubApi((c) =>
      c.path === "/api/devservers"
        ? { body: { servers: [server({}), server({ id: "s2", repoName: "shop-api", command: "go run .", status: "orphaned", ports: [] })] } }
        : undefined,
    );
    await renderWidget(RunningServices);
    const items = await screen.findAllByRole("listitem");
    expect(items[0]?.textContent).toContain("shop-api");
    expect(items[0]?.textContent).toContain("Orphaned");
    expect(screen.getByRole("link", { name: "127.0.0.1:41002" }).getAttribute("href")).toBe("http://127.0.0.1:41002");
  });

  it("stops an orphan from its row", async () => {
    const api = stubApi((c) =>
      c.path === "/api/devservers" ? { body: { servers: [server({ id: "s2", status: "orphaned", ports: [] })] } } : undefined,
    );
    await renderWidget(RunningServices);
    fireEvent.click(await screen.findByRole("button", { name: /^Stop/ }));
    await waitFor(() => { expect(api.calls.some((c) => c.method === "POST" && c.path === "/api/devservers/s2/stop")).toBe(true); });
  });

  it("warns about a port outside the block", async () => {
    stubApi((c) =>
      c.path === "/api/devservers" ? { body: { servers: [server({ ports: [{ port: 3000, announced: false, outsideBlock: true }] })] } } : undefined,
    );
    await renderWidget(RunningServices);
    expect(await screen.findByText(/Outside this worktree's port block/)).toBeTruthy();
  });

  it("updates live from the topic", async () => {
    stubApi((c) => (c.path === "/api/devservers" ? { body: { servers: [] } } : undefined));
    const view = await renderWidget(RunningServices);
    expect(await screen.findByText("No dev servers yet.")).toBeTruthy();
    act(() => {
      view.push("devservers", { server: server({}) });
    });
    expect(await screen.findByText("Running")).toBeTruthy();
  });

  it("shows an error", async () => {
    stubApi(() => ({ status: 500, body: { code: "internal", message: "Something went wrong.", hint: "Try again." } }));
    await renderWidget(RunningServices);
    expect((await screen.findByRole("alert")).textContent).toContain("Something went wrong.");
  });
});
