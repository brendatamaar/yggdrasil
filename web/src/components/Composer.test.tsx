import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { WSClient } from "../api/ws";
import { useComposerUi } from "../features/composer/store";
import { createAppRouter } from "../router";
import { stubApi, type Handler } from "../test/fakeApi";
import { FakeSocket } from "../test/fakeSocket";

const counts = { all: 1, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 0, problems: 0 };
const projects = [
  {
    id: "prj_1",
    name: "acme-shop",
    path: "x",
    counts,
    repos: [
      { id: "rep_1", name: "shop-api", flags: [], conflicted: 0, ahead: 0, behind: 0, staged: 0, unstaged: 0, untracked: 0, failedPipeline: false },
      { id: "rep_2", name: "shop-web", flags: [], conflicted: 0, ahead: 0, behind: 0, staged: 0, unstaged: 0, untracked: 0, failedPipeline: false },
    ],
  },
];

function withRepos(h?: Handler): Handler {
  return (c) => {
    if (c.path.startsWith("/api/repos?")) return { body: { projects, counts } };
    if (c.path.startsWith("/api/composer/models")) return { body: { agent: "mock", models: [{ id: "demo-1", label: "Demo 1" }] } };
    if (c.path === "/api/composer/prompts") return { body: { prompts: [] } };
    return h?.(c);
  };
}

function renderHome(handler?: Handler) {
  const api = stubApi(withRepos(handler));
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
  return api;
}

const promptBox = () => screen.getByLabelText("What should the agent look into?");
const start = () => screen.getByRole<HTMLButtonElement>("button", { name: "Start task" });

describe("composer", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
    localStorage.clear();
    useComposerUi.setState({ writeModesOff: false, acknowledged: [] });
  });

  it("keeps Start disabled, with a reason, until the prompt is written", async () => {
    renderHome();
    await screen.findByRole("option", { name: "acme-shop" }, { timeout: 8000 });
    expect(start().disabled).toBe(true);
    expect(screen.getByText("Write what the agent should do.")).toBeTruthy();
    fireEvent.change(promptBox(), { target: { value: "Look at the cart" } });
    expect(start().disabled).toBe(false);
  });

  it("creates an Ask-mode task with the demo agent and opens its transcript", async () => {
    const api = renderHome((c) => {
      if (c.method === "POST" && c.path === "/api/tasks") {
        return { status: 201, body: { task: { id: "tsk_9", title: "Look at the cart", status: "running" } } };
      }
      if (c.path === "/api/tasks/tsk_9") return { body: { task: { id: "tsk_9", title: "Look at the cart", status: "running" } } };
      return undefined;
    });
    await screen.findByRole("option", { name: "acme-shop" }, { timeout: 8000 });
    fireEvent.change(promptBox(), { target: { value: "Look at the cart" } });
    fireEvent.click(start());

    expect((await screen.findByRole("heading", { name: "Look at the cart" })).textContent).toBe("Look at the cart");
    expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({
      projectId: "prj_1",
      repoIds: ["rep_1"],
      scope: "repo",
      prompt: "Look at the cart",
      mode: "ask",
      agent: "mock",
    });
    const saved = localStorage.getItem("ygg.composer") ?? "";
    expect(saved).toContain("rep_1");
    expect(saved).not.toContain("Look at the cart");
  });

  it("shows the server's message and hint when the task is refused", async () => {
    renderHome((c) =>
      c.method === "POST"
        ? { status: 400, body: { code: "bad_request", message: "The agent \"x\" isn't available.", hint: "Pick one of the agents listed in Settings." } }
        : undefined,
    );
    await screen.findByRole("option", { name: "acme-shop" }, { timeout: 8000 });
    fireEvent.change(promptBox(), { target: { value: "Look" } });
    fireEvent.click(start());
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("isn't available");
    expect(alert.textContent).toContain("Pick one of the agents");
    expect(promptBox()).toHaveProperty("value", "Look");
  });

  it("shows the empty-project state", async () => {
    stubApi((c) => (c.path.startsWith("/api/repos?") ? { body: { projects: [], counts } } : undefined));
    const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
    render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
    expect(await screen.findByText(/No projects yet/)).toBeTruthy();
    fireEvent.change(promptBox(), { target: { value: "x" } });
    expect(start().disabled).toBe(true);
    expect(screen.getByText("Pick a project.")).toBeTruthy();
  });
});
