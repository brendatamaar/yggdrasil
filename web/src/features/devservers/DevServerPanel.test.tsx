import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DevServerDTO } from "../../api/gen/api.gen";
import { stubApi, type Handler } from "../../test/fakeApi";
import { renderWithWS } from "../../test/renderWithWS";
import { DevServerPanel } from "./DevServerPanel";

const server = (over: Partial<DevServerDTO> = {}): DevServerDTO => ({
  id: "s1",
  repoId: "r1",
  repoName: "shop-web",
  command: "npm run dev",
  status: "running",
  ports: [{ port: 41002, announced: true, outsideBlock: false }],
  ...over,
});

function setup(servers: DevServerDTO[], extra?: Handler) {
  const api = stubApi((c) => {
    const custom = extra?.(c);
    if (custom) return custom;
    if (c.path === "/api/devservers") return { body: { servers } };
    return undefined;
  });
  const view = renderWithWS(<DevServerPanel scope={{ repoId: "r1" }} />);
  return { api, ...view };
}

describe("DevServerPanel", () => {
  it("shows state, ports as links and the buttons a running server allows", async () => {
    setup([server()]);
    expect(await screen.findByText("Running")).toBeTruthy();
    const link = screen.getByRole("link", { name: "127.0.0.1:41002" });
    expect(link.getAttribute("href")).toBe("http://127.0.0.1:41002");
    expect(screen.getByRole("button", { name: "Stop npm run dev" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Restart npm run dev" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Start npm run dev" })).toBeNull();
  });

  it("warns about a port outside the worktree's block", async () => {
    setup([server({ ports: [{ port: 3000, announced: false, outsideBlock: true }] })]);
    expect(await screen.findByText(/Outside this worktree's port block/)).toBeTruthy();
  });

  it("only lists servers of this repo's main checkout", async () => {
    setup([server(), server({ id: "s2", worktreeId: "w1", command: "npm run other" }), server({ id: "s3", repoId: "r9", command: "go run ." })]);
    await screen.findByText("npm run dev");
    expect(screen.queryByText("npm run other")).toBeNull();
    expect(screen.queryByText("go run .")).toBeNull();
  });

  it("starts a stopped server with a POST and shows a failed one's reason", async () => {
    const h = setup([server({ status: "failed", ports: [], message: "The port was already in use." })]);
    expect(await screen.findByText("The port was already in use.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Start npm run dev" }));
    await waitFor(() => { expect(h.api.calls.some((c) => c.method === "POST" && c.path === "/api/devservers/s1/start")).toBe(true); });
  });

  it("shows an action error in plain words", async () => {
    setup([server()], (c) =>
      c.path === "/api/devservers/s1/stop" ? { status: 409, body: { code: "conflict", message: "It is already stopping.", hint: "Wait a moment." } } : undefined,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Stop npm run dev" }));
    expect((await screen.findByRole("alert")).textContent).toContain("It is already stopping.");
  });

  it("updates from the hub topic", async () => {
    const h = setup([server()]);
    await screen.findByText("Running");
    act(() => {
      h.push("devservers", { server: server({ status: "stopped", ports: [] }) });
    });
    expect(await screen.findByText("Stopped")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start npm run dev" })).toBeTruthy();
  });

  it("shows the log tail, appends streamed lines, and notes an incomplete log", async () => {
    const h = setup([server()], (c) =>
      c.path === "/api/devservers/s1/log?tail=200"
        ? { body: { incomplete: true, lines: [{ id: "s1", seq: 1, stream: "stdout", line: "ready on 41002" }] } }
        : undefined,
    );
    fireEvent.click(await screen.findByRole("button", { name: /Show log/ }));
    const log = await screen.findByRole("log", { name: "Log of npm run dev" });
    expect(within(log).getByText("ready on 41002")).toBeTruthy();
    expect(screen.getByText(/Some output was lost/)).toBeTruthy();
    act(() => {
      h.push("devserver:s1", { id: "s1", seq: 2, stream: "stderr", line: "warning: slow start" });
      h.push("devserver:s1", { id: "s1", seq: 2, stream: "stderr", line: "warning: slow start" });
    });
    expect(await within(log).findAllByText("warning: slow start")).toHaveLength(1);
  });

  it("pauses auto-scroll when the person scrolls up and offers to resume", async () => {
    setup([server()], (c) =>
      c.path === "/api/devservers/s1/log?tail=200" ? { body: { incomplete: false, lines: [{ id: "s1", seq: 1, stream: "stdout", line: "x" }] } } : undefined,
    );
    fireEvent.click(await screen.findByRole("button", { name: /Show log/ }));
    const log = await screen.findByRole("log");
    Object.defineProperty(log, "scrollHeight", { value: 1000, configurable: true });
    Object.defineProperty(log, "clientHeight", { value: 100, configurable: true });
    log.scrollTop = 0;
    fireEvent.scroll(log);
    const resume = await screen.findByRole("button", { name: "Resume auto-scroll" });
    fireEvent.click(resume);
    await waitFor(() => { expect(screen.queryByRole("button", { name: "Resume auto-scroll" })).toBeNull(); });
  });

  it("shows a load error", async () => {
    setup([], () => ({ status: 500, body: { code: "internal", message: "Something went wrong.", hint: "Try again." } }));
    expect((await screen.findByRole("alert")).textContent).toContain("Something went wrong.");
  });

  it("starts a never-run placeholder row and sends the target in the body", async () => {
    const placeholder = server({ id: "ph1", status: "stopped", ports: [], command: "npm run dev" });
    const h = setup([placeholder]);
    fireEvent.click(await screen.findByRole("button", { name: "Start npm run dev" }));
    await waitFor(() => { expect(h.api.calls.some((c) => c.path === "/api/devservers/ph1/start")).toBe(true); });
    const call = h.api.calls.find((c) => c.path === "/api/devservers/ph1/start");
    expect(call?.method).toBe("POST");
    expect(call?.body).toEqual({ repoId: "r1", command: "npm run dev" });
  });

  it("includes the worktree in the body for a worktree row", async () => {
    const h = stubApi((c) =>
      c.path === "/api/devservers" ? { body: { servers: [server({ id: "ph2", status: "stopped", ports: [], worktreeId: "w1" })] } } : undefined,
    );
    renderWithWS(<DevServerPanel scope={{ repoId: "r1", worktreeIds: ["w1"] }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Start npm run dev" }));
    await waitFor(() => { expect(h.calls.some((c) => c.path === "/api/devservers/ph2/start")).toBe(true); });
    expect(h.calls.find((c) => c.path === "/api/devservers/ph2/start")?.body).toEqual({ repoId: "r1", worktreeId: "w1", command: "npm run dev" });
  });

  it("offers Stop for a server that is stopping, and sends the body", async () => {
    const h = setup([server({ status: "stopping" })]);
    fireEvent.click(await screen.findByRole("button", { name: "Stop npm run dev" }));
    await waitFor(() => { expect(h.api.calls.some((c) => c.path === "/api/devservers/s1/stop")).toBe(true); });
    expect(screen.queryByRole("button", { name: "Restart npm run dev" })).toBeNull();
  });

  it("a tail refetch after live lines gives no duplicates and no reordering", async () => {
    let tail = [1, 2];
    const mk = (n: number) => ({ id: "s1", seq: n, stream: "stdout", line: `line ${String(n)}` });
    const h = setup([server()], (c) =>
      c.path === "/api/devservers/s1/log?tail=200" ? { body: { incomplete: false, lines: tail.map(mk) } } : undefined,
    );
    fireEvent.click(await screen.findByRole("button", { name: /Show log/ }));
    const log = await screen.findByRole("log");
    expect(await within(log).findByText("line 2")).toBeTruthy();
    // Live lines 4 then 3 arrive (out of order), then the hub says lines were missed.
    act(() => {
      h.push("devserver:s1", mk(4));
      h.push("devserver:s1", mk(3));
    });
    await within(log).findByText("line 4");
    tail = [2, 3, 4, 5];
    act(() => {
      h.socket.receive({ type: "gap", topic: "devserver:s1" });
    });
    await within(log).findByText("line 5");
    const texts = Array.from(log.querySelectorAll("div")).map((d) => d.textContent);
    expect(texts).toEqual(["line 1", "line 2", "line 3", "line 4", "line 5"]);
  });
});
