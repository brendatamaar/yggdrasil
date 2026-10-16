import { createMemoryHistory } from "@tanstack/react-router";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../App";
import { WSClient } from "../api/ws";
import { createAppRouter } from "../router";
import { stubApi, type Handler } from "../test/fakeApi";
import { FakeSocket } from "../test/fakeSocket";

function renderHome(handler?: Handler) {
  const api = stubApi(handler);
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
  return api;
}

function fill(prompt: string, projectId: string, repoId: string) {
  fireEvent.change(screen.getByLabelText("What should the agent look into?"), { target: { value: prompt } });
  fireEvent.change(screen.getByLabelText("Project ID"), { target: { value: projectId } });
  fireEvent.change(screen.getByLabelText("Repo ID"), { target: { value: repoId } });
}

describe("composer", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
  });

  it("keeps Start disabled until there is a prompt, a project and a repo", async () => {
    renderHome();
    const start = await screen.findByRole("button", { name: "Start task" });
    expect((start as HTMLButtonElement).disabled).toBe(true);
    fill("Look at the cart", "prj_1", "");
    expect((start as HTMLButtonElement).disabled).toBe(true);
    fill("Look at the cart", "prj_1", "rep_1");
    expect((start as HTMLButtonElement).disabled).toBe(false);
  });

  it("creates an Ask-mode task with the demo agent and opens its transcript", async () => {
    const api = renderHome((c) => {
      if (c.method === "POST" && c.path === "/api/tasks") {
        return { status: 201, body: { task: { id: "tsk_9", title: "Look at the cart", status: "running" } } };
      }
      if (c.path === "/api/tasks/tsk_9") return { body: { task: { id: "tsk_9", title: "Look at the cart", status: "running" } } };
      return undefined;
    });
    await screen.findByRole("button", { name: "Start task" });
    fill("Look at the cart", "prj_1", "rep_1");
    fireEvent.click(screen.getByRole("button", { name: "Start task" }));

    expect((await screen.findByRole("heading", { name: "Look at the cart" })).textContent).toBe("Look at the cart");
    const post = api.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({
      projectId: "prj_1",
      repoIds: ["rep_1"],
      prompt: "Look at the cart",
      mode: "ask",
      agent: "mock",
    });
    // The IDs are remembered for next time, the prompt is not.
    const saved = localStorage.getItem("ygg.composer") ?? "";
    expect(saved).toContain("rep_1");
    expect(saved).not.toContain("Look at the cart");
  });

  it("sends the name the person typed, and only that, as the title", async () => {
    const created = { task: { id: "tsk_9", title: "Cart review", status: "running" } };
    const api = renderHome((c) => {
      if (c.method === "POST") return { status: 201, body: created };
      if (c.path === "/api/tasks/tsk_9") return { body: created };
      return undefined;
    });
    await screen.findByRole("button", { name: "Start task" });
    fill("Look at src/secret.go and token=abc", "prj_1", "rep_1");
    fireEvent.change(screen.getByLabelText("Task name (optional)"), { target: { value: "  Cart review  " } });
    fireEvent.click(screen.getByRole("button", { name: "Start task" }));
    await screen.findByRole("heading", { name: "Cart review" });
    const body = api.calls.find((c) => c.method === "POST")?.body as { title?: string; prompt: string };
    expect(body.title).toBe("Cart review");
    expect(body.prompt).toContain("src/secret.go");
  });

  it("shows the server's message and hint when the task is refused", async () => {
    renderHome((c) =>
      c.method === "POST"
        ? { status: 400, body: { code: "bad_request", message: "The agent \"x\" isn't available.", hint: "Pick one of the agents listed in Settings." } }
        : undefined,
    );
    await screen.findByRole("button", { name: "Start task" });
    fill("Look", "prj_1", "rep_1");
    fireEvent.click(screen.getByRole("button", { name: "Start task" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("isn't available");
    expect(alert.textContent).toContain("Pick one of the agents");
    expect(screen.getByLabelText("What should the agent look into?")).toHaveProperty("value", "Look");
  });

  it("tells the person that only Ask mode exists", async () => {
    renderHome();
    expect(await screen.findByText(/Ask mode: the agent can read, not change\./)).toBeTruthy();
  });
});
