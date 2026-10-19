import { createMemoryHistory } from "@tanstack/react-router";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import type { BuildDTO, BuildPresetDTO, BuildStepDTO } from "../../api/gen/api.gen";
import { createAppRouter } from "../../router";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";
import { splitArgs } from "./ui";

const now = new Date().toISOString();
const counts = { all: 2, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 0, problems: 0 };
const projects = [
  {
    id: "p1",
    name: "acme-shop",
    path: "/work/acme-shop",
    counts,
    repos: [
      { id: "r1", name: "shop-api" },
      { id: "r2", name: "shop-web" },
    ],
  },
];

const step = (over: Partial<BuildStepDTO>): BuildStepDTO => ({
  index: 0,
  repoId: "r1",
  name: "test",
  status: "succeeded",
  outcome: "succeeded",
  argv: ["go", "test"],
  attempt: 1,
  logTruncated: false,
  ...over,
});

const build = (over: Partial<BuildDTO>): BuildDTO => ({
  id: "b1",
  status: "succeeded",
  target: "main",
  actor: "user",
  queuedAt: now,
  startedAt: now,
  finishedAt: now,
  steps: [step({})],
  artifactsCollected: true,
  artifacts: [],
  ...over,
});

const preset: BuildPresetDTO = {
  id: "pr1",
  projectId: "p1",
  name: "Full check",
  steps: [{ repoId: "r1", name: "test", argv: ["go", "test"] }],
};

function base(c: Call): Reply | undefined {
  if (c.path.startsWith("/api/repos?")) return { body: { projects, counts } };
  if (c.path.startsWith("/api/build-presets")) return { body: { presets: [preset] } };
  return undefined;
}

function renderAt(path: string, handler: (c: Call) => Reply | undefined) {
  const api = stubApi((c) => handler(c) ?? base(c));
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
  FakeSocket.instances = [];
  const socket = new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket });
  render(<App router={router} socket={socket} />);
  return api;
}

describe("splitArgs", () => {
  it("splits on spaces and keeps quoted words", () => {
    expect(splitArgs('go test -run "a b"')).toEqual(["go", "test", "-run", "a b"]);
  });
});

describe("builds list", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
  });

  it("lists builds, filters by status and refetches on the builds topic", async () => {
    let list = [build({})];
    const { calls } = renderAt("/builds", (c) =>
      c.path.startsWith("/api/builds") ? { body: { builds: list } } : undefined,
    );
    expect(await screen.findByText("Succeeded")).toBeTruthy();
    expect(screen.getByText("shop-api")).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "failed" } });
    await waitFor(() => {
      expect(calls.some((c) => c.path === "/api/builds?status=failed")).toBe(true);
    });

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "" } });
    await screen.findByText("Succeeded");
    list = [build({ id: "b2", status: "running", finishedAt: undefined })];
    act(() => {
      FakeSocket.last().open();
      FakeSocket.last().receive({ type: "event", topic: "builds", seq: 1, data: { buildId: "b2", status: "running" } });
    });
    expect(await screen.findByText("Running")).toBeTruthy();
  });

  it("starts a build from argv rows and shows the server refusal", async () => {
    const { calls } = renderAt("/builds", (c) => {
      if (c.method === "POST" && c.path === "/api/builds") {
        return {
          status: 400,
          body: { code: "bad_request", message: "Shell syntax such as && isn't supported.", hint: "Split it into steps." },
        };
      }
      if (c.path.startsWith("/api/builds")) return { body: { builds: [] } };
      return undefined;
    });
    fireEvent.click(await screen.findByRole("button", { name: "New build" }));
    const dialog = await screen.findByRole("dialog");
    const start = within(dialog).getByRole("button", { name: "Start build" });
    expect((start as HTMLButtonElement).disabled).toBe(true);
    await within(dialog).findByRole("option", { name: "shop-api" });
    fireEvent.change(within(dialog).getByLabelText("Step 1 repo"), { target: { value: "r1" } });
    fireEvent.change(within(dialog).getByLabelText("Step 1 name"), { target: { value: "test" } });
    fireEvent.change(within(dialog).getByLabelText("Step 1 command"), { target: { value: "go test && go vet" } });
    fireEvent.click(start);
    expect(await within(dialog).findByRole("alert")).toHaveProperty("textContent", expect.stringContaining("isn't supported"));
    const post = calls.find((c) => c.method === "POST" && c.path === "/api/builds");
    expect(post?.body).toEqual({
      target: "main",
      steps: [{ repoId: "r1", name: "test", argv: ["go", "test", "&&", "go", "vet"] }],
    });
  });

  it("runs a preset and creates one", async () => {
    const { calls } = renderAt("/builds", (c) => {
      if (c.method === "POST" && c.path === "/api/build-presets/pr1/run") return { status: 202, body: { buildId: "b9" } };
      if (c.method === "POST" && c.path === "/api/build-presets") return { status: 201, body: preset };
      if (c.path.startsWith("/api/builds")) return { body: { builds: [] } };
      return undefined;
    });
    fireEvent.click(await screen.findByRole("button", { name: "New preset" }));
    const dialog = await screen.findByRole("dialog");
    await within(dialog).findByRole("option", { name: "shop-api" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Full check" } });
    fireEvent.change(within(dialog).getByLabelText("Step 1 repo"), { target: { value: "r1" } });
    fireEvent.change(within(dialog).getByLabelText("Step 1 name"), { target: { value: "test" } });
    fireEvent.change(within(dialog).getByLabelText("Step 1 command"), { target: { value: "go test" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save preset" }));
    await waitFor(() => {
      expect(calls.find((c) => c.method === "POST" && c.path === "/api/build-presets")?.body).toEqual({
        projectId: "p1",
        name: "Full check",
        steps: [{ repoId: "r1", name: "test", argv: ["go", "test"] }],
      });
    });
    fireEvent.click(await screen.findByRole("button", { name: "Run Full check" }));
    await waitFor(() => {
      expect(calls.some((c) => c.path === "/api/build-presets/pr1/run")).toBe(true);
    });
  });
});

describe("build page", () => {
  it("appends live log lines, cancels, and explains a start failure", async () => {
    const running = build({
      status: "running",
      finishedAt: undefined,
      steps: [
        step({ status: "running", outcome: undefined }),
        step({ index: 1, name: "lint", status: "failed", outcome: "start_failed", startFailure: "not_found", logTruncated: true }),
      ],
    });
    const { calls } = renderAt("/builds/b1", (c) => {
      if (c.path === "/api/builds/b1") return { body: running };
      if (c.path === "/api/builds/b1/cancel") return { status: 202, body: running };
      if (c.path.startsWith("/api/builds/b1/steps/0/log")) return { body: { data: "first\n", next: 6 } };
      return undefined;
    });
    expect(await screen.findByText("first")).toBeTruthy();
    act(() => {
      FakeSocket.last().open();
      FakeSocket.last().receive({
        type: "event",
        topic: "build:b1",
        seq: 1,
        data: { buildId: "b1", step: 0, attempt: 1, line: "second" },
      });
    });
    expect(await screen.findByText("second")).toBeTruthy();
    expect(screen.getByText(/Could not start\. The program wasn't found/)).toBeTruthy();
    expect(screen.getByText("Log cut off")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Cancel build" }));
    await waitFor(() => {
      expect(calls.some((c) => c.method === "POST" && c.path === "/api/builds/b1/cancel")).toBe(true);
    });
  });

  it("lists artifacts after success", async () => {
    renderAt("/builds/b1", (c) =>
      c.path === "/api/builds/b1"
        ? { body: build({ artifacts: [{ repoId: "r1", path: "dist/app.zip", size: 2048 }] }) }
        : c.path.includes("/log")
          ? { body: { data: "", next: 0 } }
          : undefined,
    );
    expect(await screen.findByText("dist/app.zip")).toBeTruthy();
    expect(screen.getByText("2.0 KB")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Cancel build" })).toBeNull();
  });
});
