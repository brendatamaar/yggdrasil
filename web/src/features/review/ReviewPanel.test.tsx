import type { WorktreeDiffFileDTO } from "../../api/gen/api.gen";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { stubApi, type Handler } from "../../test/fakeApi";
import { mixedDiff } from "./fixtures";
import { ReviewPanel } from "./ReviewPanel";

const verif = (over: Record<string, unknown> = {}) => ({
  id: "v1",
  taskId: "t1",
  repoId: "r1",
  repoName: "shop-api",
  worktreeId: "w1",
  state: "passed",
  attempt: 1,
  ...over,
});
const note = (over: Record<string, unknown> = {}) => ({
  id: "n1",
  taskId: "t1",
  repoId: "r1",
  path: "src/a.ts",
  line: 10,
  side: "new",
  text: "Rename this",
  state: "open",
  createdAt: "2026-10-30T10:00:00Z",
  ...over,
});
const summary = { state: "passed", total: 1, passed: 1, failed: 0, running: 0 };

interface Setup {
  verifications?: unknown[];
  notes?: unknown[];
  diff?: string;
  files?: WorktreeDiffFileDTO[];
  extra?: Handler;
}

function setup(o: Setup = {}) {
  const api = stubApi((c) => {
    const custom = o.extra?.(c);
    if (custom) return custom;
    if (c.path === "/api/tasks/t1/verifications") return { body: { verifications: o.verifications ?? [verif()], summary } };
    if (c.path === "/api/tasks/t1/notes" && c.method === "GET") return { body: { notes: o.notes ?? [] } };
    return undefined;
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ReviewPanel taskId="t1" repoIds={["r1"]} loadDiff={() => Promise.resolve({ text: o.diff ?? mixedDiff, truncated: false, binary: false, files: o.files ?? [] })} />
    </QueryClientProvider>,
  );
  return api;
}

describe("verification badges", () => {
  const cases: [string, string][] = [
    ["skipped", "Skipped"],
    ["unverifiable", "Can't verify"],
    ["bootstrapping", "Setting up"],
    ["verifying", "Verifying"],
    ["passed", "Passed"],
    ["failed", "Failed"],
    ["interrupted", "Interrupted"],
    ["cancelled", "Cancelled"],
  ];
  it.each(cases)("renders %s", async (state, label) => {
    setup({ verifications: [verif({ state, attempt: 2 })] });
    const row = await screen.findByLabelText("Verification of shop-api");
    expect(row.textContent).toContain(`${label} · attempt 2`);
  });

  it("shows the log tail of a failed step and sends the failure to the agent", async () => {
    const api = setup({
      verifications: [verif({ state: "failed", failedStep: "test", exitCode: 1, message: "FAIL order total", canSendFailure: true })],
    });
    expect((await screen.findByLabelText("Log tail of shop-api")).textContent).toBe("FAIL order total");
    fireEvent.click(screen.getByRole("button", { name: "Send failure to agent" }));
    await waitFor(() => {
      expect(api.calls).toContainEqual(expect.objectContaining({ method: "POST", path: "/api/verifications/v1/send-failure" }));
    });
  });

  it("offers no Send failure when the server says it can't", async () => {
    setup({ verifications: [verif({ state: "failed", message: "x" })] });
    await screen.findByLabelText("Log tail of shop-api");
    expect(screen.queryByRole("button", { name: "Send failure to agent" })).toBeNull();
  });

  it("verifies again", async () => {
    const api = setup();
    const btn = await screen.findByRole("button", { name: "Verify again" });
    fireEvent.click(btn);
    await waitFor(() => {
      expect(api.calls).toContainEqual(expect.objectContaining({ method: "POST", path: "/api/tasks/t1/verify/retry" }));
    });
  });

  it("is keyboard operable: the actions are real buttons that can take focus", async () => {
    setup({ verifications: [verif({ state: "failed", message: "x", canSendFailure: true })] });
    const btn = await screen.findByRole("button", { name: "Send failure to agent" });
    btn.focus();
    expect(document.activeElement).toBe(btn);
  });
});

describe("diff and notes", () => {
  it("hides line-ending-only hunks and shows the chip", async () => {
    setup();
    expect(await screen.findByText("line endings changed")).toBeTruthy();
    expect(screen.getByText("fresh()", { exact: false })).toBeTruthy();
    expect(screen.queryByText("const a = 1;")).toBeNull();
  });

  it("says so when a file only changed line endings", async () => {
    setup({ diff: "diff --git a/x b/x\n@@ -1 +1 @@\n-a\r\n+a\n" });
    expect(await screen.findByText(/Only line endings changed/)).toBeTruthy();
  });

  it("adds a note on a line with the diff hash", async () => {
    const api = setup({
      extra: (c) => (c.method === "POST" && c.path === "/api/tasks/t1/notes" ? { body: note() } : undefined),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Add note on line 10 of src/a.ts" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Note" }), { target: { value: "Rename this" } });
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() => {
      const post = api.calls.find((c) => c.method === "POST" && c.path === "/api/tasks/t1/notes");
      expect(post?.body).toMatchObject({ repoId: "r1", path: "src/a.ts", line: 10, side: "new", text: "Rename this" });
      expect((post?.body as { diffHash: string }).diffHash).toMatch(/^[0-9a-f]{64}$/);
    });
  });

  it("edits and deletes a note", async () => {
    const api = setup({ notes: [note()] });
    fireEvent.click(await screen.findByRole("button", { name: "Edit note" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Note" }), { target: { value: "Better name" } });
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() => {
      expect(api.calls).toContainEqual(expect.objectContaining({ method: "PUT", path: "/api/notes/n1", body: { text: "Better name" } }));
    });
    fireEvent.click(await screen.findByRole("button", { name: "Delete note" }));
    await waitFor(() => {
      expect(api.calls).toContainEqual(expect.objectContaining({ method: "DELETE", path: "/api/notes/n1" }));
    });
  });

  it("marks an outdated note and does not send it", async () => {
    const api = setup({ notes: [note({ state: "outdated" }), note({ id: "n2", line: 11, text: "Other", state: "open" })] });
    expect(await screen.findByText(/Outdated/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Send notes to agent (1)" }));
    await waitFor(() => {
      expect(api.calls).toContainEqual(expect.objectContaining({ path: "/api/tasks/t1/notes/send", body: { ids: ["n2"] } }));
    });
  });

  it("keeps the note text when the save fails", async () => {
    setup({
      extra: (c) =>
        c.method === "POST" && c.path === "/api/tasks/t1/notes"
          ? { status: 409, body: { code: "conflict", message: "That file changed.", hint: "Reload." } }
          : undefined,
    });
    fireEvent.click(await screen.findByRole("button", { name: "Add note on line 10 of src/a.ts" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Note" }), { target: { value: "keep me" } });
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    expect((await screen.findByRole("alert")).textContent).toContain("That file changed.");
    expect(screen.getByRole<HTMLTextAreaElement>("textbox", { name: "Note" }).value).toBe("keep me");
  });
});

describe("not ready", () => {
  it("shows calm states for 501 answers", async () => {
    const nr = { status: 501, body: { code: "not_ready", message: "Not available yet." } };
    stubApi((c) => (c.path.startsWith("/api/tasks/t1/") ? nr : undefined));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ReviewPanel taskId="t1" repoIds={["r1"]} />
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Verification results aren't available yet.")).toBeTruthy();
    expect(await screen.findByText("Notes aren't available yet.")).toBeTruthy();
    expect(await screen.findByText("Changes for this repo aren't available yet.")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

const fileDto = (over: Partial<WorktreeDiffFileDTO> = {}): WorktreeDiffFileDTO => ({
  path: "src/a.ts",
  status: "M",
  binary: false,
  added: 1,
  deleted: 1,
  eolOnly: false,
  hash: "a".repeat(64),
  patchTruncated: false,
  ...over,
});

describe("server diff", () => {
  it("anchors a note on the server's per-file hash", async () => {
    const api = setup({
      files: [fileDto()],
      extra: (c) => (c.method === "POST" && c.path === "/api/tasks/t1/notes" ? { body: note() } : undefined),
    });
    fireEvent.click(await screen.findByRole("button", { name: "Add note on line 10 of src/a.ts" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Note" }), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() => {
      const post = api.calls.find((c) => c.method === "POST" && c.path === "/api/tasks/t1/notes");
      expect((post?.body as { diffHash: string }).diffHash).toBe("a".repeat(64));
    });
  });

  it("does not allow notes on a cut patch, and lists binary files as not anchorable", async () => {
    setup({ files: [fileDto({ patchTruncated: true }), fileDto({ path: "logo.png", binary: true })] });
    const add = await screen.findByRole<HTMLButtonElement>("button", { name: "Add note on line 10 of src/a.ts" });
    expect(add.disabled).toBe(true);
    expect(screen.getByText(/patch for this file is cut short/)).toBeTruthy();
    expect(screen.getByText(/binary file/).textContent).toContain("Notes can't be added on it");
  });

  it("calls the worktree diff endpoint", async () => {
    const { loadWorktreeDiff } = await import("./api");
    const api = stubApi((c) => (c.path === "/api/tasks/t1/repos/r1/diff" ? { body: { text: "", truncated: false, binary: false, files: [] } } : undefined));
    await loadWorktreeDiff("t1", "r1");
    expect(api.calls[0]?.path).toBe("/api/tasks/t1/repos/r1/diff");
  });
});

describe("cancel cause", () => {
  it("says the repo is no longer trusted and keeps Verify again", async () => {
    setup({ verifications: [verif({ state: "cancelled", cancelCause: "not_trusted", reason: "x" })] });
    expect(await screen.findByText("Stopped: this repo is no longer trusted")).toBeTruthy();
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Verify again" }).disabled).toBe(false);
  });
});
