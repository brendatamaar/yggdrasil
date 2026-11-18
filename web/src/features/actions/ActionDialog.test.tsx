import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ActionPreviewDTO, ProposalDTO } from "../../api/gen/api.gen";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { ActionDialog } from "./ActionDialog";
import { useActionMachine } from "./hooks";

function Harness({ kind = "pull_and_check", sourceId }: { kind?: string; sourceId?: string }) {
  const machine = useActionMachine();
  return (
    <>
      <button
        type="button"
        onClick={() => {
          machine.open({ repoId: "r1", repoName: "shop-api", kind, title: "Pull and check", ...(sourceId ? { sourceId } : {}) });
        }}
      >
        Open action
      </button>
      <ActionDialog machine={machine} />
    </>
  );
}

const hash = "a".repeat(64);
const proposal = (over: Partial<ProposalDTO> = {}): ProposalDTO => ({
  id: "p1",
  repoId: "r1",
  repoName: "shop-api",
  kind: "pull_ff",
  status: "pending",
  source: "user",
  fromAgent: false,
  summary: "",
  lines: ["git pull --ff-only origin main"],
  actionHash: hash,
  attempt: 0,
  createdAt: "2026-01-01T10:00:00Z",
  ...over,
});
const previewFor = (requestId: string, over: Partial<ActionPreviewDTO> = {}): ActionPreviewDTO => ({
  requestId,
  digest: "digest-1",
  kind: "pull_and_check",
  source: "shop-api main, 2 behind origin/main",
  contextText: "Fast-forward only.",
  context: "available",
  expectedHead: "0123456789abcdef",
  ...over,
});

interface Script {
  preview?: (req: { requestId: string }) => Reply;
  execute?: (body: Record<string, unknown>) => Reply;
  proposal?: () => ProposalDTO;
  confirm?: Reply;
}
function server(s: Script) {
  return (c: Call): Reply | undefined => {
    if (c.path === "/api/actions/preview") return (s.preview ?? ((r) => ({ body: previewFor(r.requestId) })))(c.body as { requestId: string });
    if (c.path === "/api/actions/execute") {
      return (s.execute ?? ((b) => ({ body: { requestId: b.requestId, state: "completed", message: "Created a pull proposal.", proposalIds: ["p1"] } })))(
        c.body as Record<string, unknown>,
      );
    }
    if (c.path === "/api/proposals/p1" && c.method === "GET") return { body: (s.proposal ?? proposal)() };
    if (c.path === "/api/proposals/p1/confirm") return s.confirm ?? { body: proposal({ status: "confirmed" }) };
    return undefined;
  };
}

const openAndPreview = async () => {
  fireEvent.click(screen.getByRole("button", { name: "Open action" }));
  await screen.findByText("shop-api main, 2 behind origin/main");
};

describe("action dialog", () => {
  it("shows scope, mode, source and what will happen before anything starts", async () => {
    const api = stubApi(server({}));
    await renderWidget(Harness);
    await openAndPreview();
    const dlg = screen.getByRole("dialog");
    expect(within(dlg).getByText("shop-api")).toBeTruthy(); // scope
    expect(within(dlg).getByText("No agent turn starts.")).toBeTruthy(); // mode of a pull
    expect(within(dlg).getByText(/never a merge/)).toBeTruthy();
    expect(within(dlg).getByLabelText("Attached context").textContent).toBe("Fast-forward only.");
    expect(api.calls.filter((c) => c.path === "/api/actions/execute")).toHaveLength(0);
  });

  it("an Ask preview says the agent cannot change files; a Code preview says it uses a worktree", async () => {
    stubApi(server({ preview: (r) => ({ body: previewFor(r.requestId, { kind: "diagnose_repo", mode: "ask" }) }) }));
    const a = await renderWidget(() => <Harness kind="diagnose_repo" />);
    await openAndPreview();
    expect(screen.getByText(/cannot change your files/)).toBeTruthy();
    a.unmount();
    stubApi(server({ preview: (r) => ({ body: previewFor(r.requestId, { kind: "fix_pipeline", mode: "code" }) }) }));
    await renderWidget(() => <Harness kind="fix_pipeline" />);
    await openAndPreview();
    expect(screen.getByText(/isolated worktree/)).toBeTruthy();
  });

  it("submits the SAME request ID with the preview's digest and head, then ends in the proposal confirmation", async () => {
    const api = stubApi(server({}));
    await renderWidget(Harness);
    await openAndPreview();
    const previewReq = api.calls.find((c) => c.path === "/api/actions/preview")?.body as { requestId: string };
    fireEvent.click(screen.getByRole("button", { name: "Create pull proposal" }));
    await screen.findByText("Created a pull proposal.");

    const exec = api.calls.filter((c) => c.path === "/api/actions/execute");
    expect(exec).toHaveLength(1);
    expect(exec[0]?.body).toMatchObject({
      requestId: previewReq.requestId,
      repoId: "r1",
      kind: "pull_and_check",
      digest: "digest-1",
      expectedHead: "0123456789abcdef",
    });

    // The pull is a proposal. It is confirmed through the shared proposal review; this dialog has no execute button.
    const review = await screen.findByRole("button", { name: "Review and confirm" });
    expect(screen.getByText("Pull (fast-forward only)")).toBeTruthy();
    expect(api.calls.filter((c) => c.path.endsWith("/confirm"))).toHaveLength(0);
    fireEvent.click(review);
    const alert = await screen.findByRole("alertdialog");
    expect(within(alert).getByText("git pull --ff-only origin main")).toBeTruthy();
    fireEvent.click(within(alert).getByRole("button", { name: "Confirm" }));
    await waitFor(() => {
      expect(api.calls.filter((c) => c.path === "/api/proposals/p1/confirm")).toHaveLength(1);
    });
    expect(api.calls.find((c) => c.path === "/api/proposals/p1/confirm")?.body).toEqual({ hash });
  });

  it("shows a pull refusal (head changed, busy) from the proposal in plain words", async () => {
    stubApi(
      server({ proposal: () => proposal({ status: "failed", failKind: "stale" }) }),
    );
    await renderWidget(Harness);
    await openAndPreview();
    fireEvent.click(screen.getByRole("button", { name: "Create pull proposal" }));
    await screen.findByText(/The branch moved after this was proposed/);
  });

  it("shows a refused result and offers a fresh preview", async () => {
    stubApi(
      server({ execute: (b) => ({ body: { requestId: b.requestId, state: "refused", refusal: "head_changed", message: "The head changed. Preview again." } }) }),
    );
    await renderWidget(Harness);
    await openAndPreview();
    fireEvent.click(screen.getByRole("button", { name: "Create pull proposal" }));
    expect((await screen.findByRole("alert")).textContent).toBe("The head changed. Preview again.");
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });

  it("a preview failure is an error with retry, not an empty preview", async () => {
    stubApi(server({ preview: () => ({ status: 503, body: { code: "unavailable", message: "The host is not reachable.", hint: "Try again." } }) }));
    await renderWidget(Harness);
    fireEvent.click(screen.getByRole("button", { name: "Open action" }));
    expect((await screen.findByRole("alert")).textContent).toContain("The host is not reachable.");
    expect(screen.queryByRole("button", { name: "Create pull proposal" })).toBeNull();
  });

  it("rejects a malformed preview instead of showing it", async () => {
    stubApi(server({ preview: () => ({ body: { requestId: "x" } }) }));
    await renderWidget(Harness);
    fireEvent.click(screen.getByRole("button", { name: "Open action" }));
    expect((await screen.findByRole("alert")).textContent).toContain("unexpected");
  });

  it("an unreadable repo preview says the task waits for the folder and never claims it runs", async () => {
    stubApi(server({ preview: (r) => ({ body: previewFor(r.requestId, { kind: "diagnose_repo", waitingForFolder: true, context: "metadata_only" }) }) }));
    await renderWidget(() => <Harness kind="diagnose_repo" />);
    await openAndPreview();
    expect(screen.getByText(/wait for the folder to be restored/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save as waiting draft" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Start diagnosis" })).toBeNull();
  });

  it("labels truncated and unavailable context", async () => {
    stubApi(server({ preview: (r) => ({ body: previewFor(r.requestId, { truncated: true, unavailable: ["no token for the host"], context: "metadata_only" }) }) }));
    await renderWidget(Harness);
    await openAndPreview();
    expect(screen.getByText("The context is cut to fit the size limit.")).toBeTruthy();
    expect(screen.getByText("Not available: no token for the host")).toBeTruthy();
    expect(screen.getByText(/Only repo facts are attached/)).toBeTruthy();
  });

  it("works from the keyboard: focus moves into the dialog, Escape closes it without sending", async () => {
    const api = stubApi(server({}));
    await renderWidget(Harness);
    await openAndPreview();
    const dlg = screen.getByRole("dialog");
    expect(dlg.contains(document.activeElement)).toBe(true);
    fireEvent.keyDown(dlg, { key: "Escape" });
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    expect(api.calls.filter((c) => c.path === "/api/actions/execute")).toHaveLength(0);
  });

  it("closing while the request is out does not cancel it, and reopening the same action does not make a second request key", async () => {
    let release: (r: Response) => void = () => undefined;
    const api = stubApi(server({}));
    const inner = globalThis.fetch;
    const execBodies: unknown[] = [];
    vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.pathname : input.url;
      if (url === "/api/actions/execute") {
        execBodies.push(JSON.parse(init?.body as string));
        return new Promise<Response>((res) => {
          release = res;
        });
      }
      return inner(input, init);
    });
    await renderWidget(Harness);
    await openAndPreview();
    fireEvent.click(screen.getByRole("button", { name: "Create pull proposal" }));
    await screen.findByText(/Sending/);
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    // Reopen: the same submission is reconciled (same key), no new preview request.
    fireEvent.click(screen.getByRole("button", { name: "Open action" }));
    await screen.findByText(/Sending/);
    expect(api.calls.filter((c) => c.path === "/api/actions/preview")).toHaveLength(1);
    const keys = new Set(execBodies.map((b) => (b as { requestId: string }).requestId));
    expect(keys.size).toBe(1);
    release(new Response(JSON.stringify({ requestId: [...keys][0], state: "completed", message: "Created a pull proposal.", proposalIds: ["p1"] }), { status: 200 }));
    await screen.findByText("Created a pull proposal.");
  });
});
