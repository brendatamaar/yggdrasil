import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { RepoEnvDTO, WorktreeEnvDTO } from "../../api/gen/api.gen";
import { stubApi, type Handler } from "../../test/fakeApi";
import { renderWithWS } from "../../test/renderWithWS";
import { RepoEnvSettings } from "./RepoEnvSettings";
import { WorktreeEnvPanel } from "./WorktreeEnvPanel";

const env: RepoEnvDTO = { repoId: "r1", profilePaths: [".env"], agentCopy: false, composeEnabled: false };
const wenv = (over: Partial<WorktreeEnvDTO> = {}): WorktreeEnvDTO => ({
  worktreeId: "w1",
  blockBase: 41000,
  blockSize: 10,
  profile: [],
  compose: "off",
  updatedAt: "2026-11-01T10:00:00Z",
  ...over,
});

describe("RepoEnvSettings", () => {
  function setup(extra?: Handler) {
    const api = stubApi((c) => {
      const custom = extra?.(c);
      if (custom) return custom;
      if (c.path === "/api/repos/r1/env" && c.method === "GET") return { body: env };
      return undefined;
    });
    renderWithWS(<RepoEnvSettings repoId="r1" />);
    return api;
  }

  it("shows the paths, the agent toggle off by default and its warning", async () => {
    setup();
    expect(await screen.findByText(".env")).toBeTruthy();
    const toggle = screen.getByRole("checkbox", { name: "Also copy for agent tasks" });
    expect((toggle as HTMLInputElement).checked).toBe(false);
    expect(screen.getByText(/agents can read these files/)).toBeTruthy();
  });

  it("adds a path with Enter, refuses bad ones with a reason, and removes one", async () => {
    setup();
    const input = await screen.findByLabelText("Add a file");
    fireEvent.change(input, { target: { value: "../outside" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect((await screen.findByRole("alert")).textContent).toContain("leave the repo");
    fireEvent.change(input, { target: { value: ".env.local" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(await screen.findByText(".env.local")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Remove .env" }));
    await waitFor(() => { expect(screen.queryByText(/^\.env$/)).toBeNull(); });
  });

  it("saves the edited settings with PUT", async () => {
    const api = setup((c) =>
      c.method === "PUT" ? { body: { ...env, agentCopy: true } } : undefined,
    );
    fireEvent.click(await screen.findByRole("checkbox", { name: "Also copy for agent tasks" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => { expect(api.calls.some((c) => c.method === "PUT")).toBe(true); });
    expect(api.calls.find((c) => c.method === "PUT")?.body).toEqual({ ...env, agentCopy: true });
    expect(await screen.findByText("Saved")).toBeTruthy();
  });

  it("asks for the compose file when compose is on and blocks saving without it", async () => {
    setup();
    fireEvent.click(await screen.findByRole("checkbox", { name: /compose project/ }));
    expect(screen.getByLabelText("Compose file")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save" }).hasAttribute("disabled")).toBe(true);
    fireEvent.change(screen.getByLabelText("Compose file"), { target: { value: "docker-compose.yml" } });
    expect(screen.getByRole("button", { name: "Save" }).hasAttribute("disabled")).toBe(false);
  });

  it("shows the server's reason when saving fails", async () => {
    setup((c) =>
      c.method === "PUT" ? { status: 400, body: { code: "invalid", message: "That path isn't allowed.", hint: "Use a path inside the repo." } } : undefined,
    );
    fireEvent.click(await screen.findByRole("checkbox", { name: "Also copy for agent tasks" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect((await screen.findByRole("alert")).textContent).toContain("That path isn't allowed.");
  });
});

describe("WorktreeEnvPanel", () => {
  function setup(initial: WorktreeEnvDTO, extra?: Handler) {
    const api = stubApi((c) => {
      const custom = extra?.(c);
      if (custom) return custom;
      if (c.path === "/api/worktrees/w1/env") return { body: initial };
      return undefined;
    });
    const view = renderWithWS(<WorktreeEnvPanel worktreeId="w1" title="shop-api" />);
    return { api, ...view };
  }

  it("shows the port block", async () => {
    setup(wenv());
    expect(await screen.findByText("41000-41009")).toBeTruthy();
  });

  it("copies profile files and lists each result", async () => {
    const h = setup(wenv(), (c) =>
      c.path === "/api/worktrees/w1/profile/copy"
        ? { body: wenv({ profile: [{ path: ".env", result: "copied" }, { path: ".env.local", result: "skipped", reason: "already there" }] }) }
        : undefined,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Copy profile files" }));
    expect(await screen.findByText(/already there/)).toBeTruthy();
    expect(h.api.calls.some((c) => c.method === "POST" && c.path === "/api/worktrees/w1/profile/copy")).toBe(true);
  });

  it("asks to review and confirm a changed compose file", async () => {
    const h = setup(wenv({ compose: "needs_confirm", composeHash: "abc123" }), (c) =>
      c.path === "/api/worktrees/w1/compose/confirm" ? { body: wenv({ compose: "down" }) } : undefined,
    );
    expect(await screen.findByText(/The compose file changed: review it and confirm/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Confirm compose file" }));
    expect(await screen.findByRole("button", { name: "Start compose" })).toBeTruthy();
    const sent = h.api.calls.find((c) => c.path === "/api/worktrees/w1/compose/confirm");
    expect(sent?.body).toEqual({ hash: "abc123" });
  });

  it("says compose is not available yet when the server answers 501", async () => {
    setup(wenv({ compose: "down" }), (c) =>
      c.path === "/api/worktrees/w1/compose/up" ? { status: 501, body: { code: "unavailable", message: "Not implemented." } } : undefined,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Start compose" }));
    expect((await screen.findByRole("alert")).textContent).toContain("isn't available yet");
  });

  it("updates from the worktreeenv topic", async () => {
    const h = setup(wenv({ blockBase: undefined, blockSize: undefined }));
    await screen.findByText("none yet");
    act(() => {
      h.push("worktreeenv", { env: wenv() });
    });
    expect(await screen.findByText("41000-41009")).toBeTruthy();
  });
});
