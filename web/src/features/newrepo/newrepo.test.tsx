import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import type { NewRepoOperationDTO, ProposalDTO } from "../../api/gen/api.gen";
import { stubApi, type Call, type Reply } from "../../test/fakeApi";
import { renderWidget } from "../../test/renderWidget";
import { NewRepoDialog } from "./NewRepoDialog";
import { useSpecMemory } from "./store";

const specHash = "a".repeat(64);
const proposalHash = "c".repeat(64);
const publishHash = "d".repeat(64);

const op = (over: Partial<NewRepoOperationDTO> = {}): NewRepoOperationDTO => ({
  operationId: "o1",
  projectId: "p1",
  repoId: "r1",
  setupTaskId: "t1",
  leaf: "shop-docs",
  state: "awaiting_confirm",
  proposalId: "pr1",
  ...over,
});
const proposal = (over: Partial<ProposalDTO> = {}): ProposalDTO => ({
  id: "pr1",
  repoId: "r1",
  repoName: "shop-docs",
  kind: "init_repo",
  status: "pending",
  source: "user",
  fromAgent: false,
  summary: "",
  lines: ["Create the folder shop-docs", "Make the first commit"],
  actionHash: proposalHash,
  attempt: 0,
  createdAt: "2026-01-01T10:00:00Z",
  ...over,
});

interface Script {
  problems?: string[];
  operation?: NewRepoOperationDTO;
  list?: NewRepoOperationDTO[];
  hosts?: { id: string; kind: string; base: string; tokenConfigured: boolean }[];
  publishProblems?: string[];
  reserve?: () => Reply;
  /** The proposals of the setup draft t1, as the server lists them. */
  setupProposals?: ProposalDTO[];
}
function server(s: Script = {}) {
  return (c: Call): Reply | undefined => {
    if (c.path === "/api/projects/p1/new-repos/preview") {
      const body = c.body as { leaf: string };
      return {
        body: {
          spec: { leaf: body.leaf, branch: "main", readme: "# docs", ignore: "node_modules/", commitMessage: "Initial commit" },
          hash: specHash,
          lines: [`Create the folder ${body.leaf} inside acme-shop`, "Write README.md and .gitignore", "Make the first commit on main"],
          ...(s.problems ? { problems: s.problems } : {}),
        },
      };
    }
    if (c.path === "/api/projects/p1/new-repos" && c.method === "POST") return s.reserve?.() ?? { body: s.operation ?? op() };
    if (c.path === "/api/projects/p1/new-repos") return { body: s.list ?? [] };
    if (c.path === "/api/new-repos/o1" && c.method === "GET") return { body: s.operation ?? op() };
    if (c.path === "/api/new-repos/o1/retry") return { body: s.operation ?? op() };
    if (c.path === "/api/hosts") return { body: { instances: s.hosts ?? [], configProblems: [], envPath: "" } };
    if (c.path === "/api/new-repos/o1/publish/preview") {
      return {
        body: {
          hash: publishHash,
          lines: ["Create a private repository acme/shop-docs on git.acme.dev", "Push main (abc1234)"],
          visibility: (c.body as { visibility?: string }).visibility ?? "private",
          expectedHead: "abc1234",
          ...(s.publishProblems ? { problems: s.publishProblems } : {}),
        },
      };
    }
    if (c.path === "/api/new-repos/o1/publish") return { body: proposal({ id: "pr2", kind: "publish", actionHash: "e".repeat(64), lines: ["Create acme/shop-docs"] }) };
    if (c.path === "/api/proposals?task=t1") return { body: { proposals: s.setupProposals ?? [] } };
    if (c.path === "/api/proposals/pr1" && c.method === "GET") return { body: proposal() };
    if (c.path === "/api/proposals/pr2" && c.method === "GET") return { body: proposal({ id: "pr2", kind: "publish", actionHash: "e".repeat(64), lines: ["Create acme/shop-docs"] }) };
    if (c.path.endsWith("/confirm")) return { body: proposal({ status: "confirmed" }) };
    return undefined;
  };
}

const Harness = () => <NewRepoDialog projectId="p1" projectName="acme-shop" open onOpenChange={() => undefined} />;

const typeLeaf = (v: string) => {
  fireEvent.change(screen.getByLabelText(/Folder name/), { target: { value: v } });
};
const previewIt = async () => {
  typeLeaf("shop-docs");
  fireEvent.click(screen.getByRole("button", { name: "Preview" }));
  await screen.findByRole("list", { name: "What will happen" });
};

beforeEach(() => {
  useSpecMemory.setState({ specs: {} });
});

describe("new repo: preview and reserve", () => {
  it("shows what will happen and the hash, and creates nothing on a preview", async () => {
    const api = stubApi(server());
    await renderWidget(Harness);
    await previewIt();
    expect(screen.getByText(/Create the folder shop-docs inside acme-shop/)).toBeTruthy();
    expect(screen.getByTestId("spec-hash").textContent).toBe(specHash);
    expect(api.calls.filter((c) => c.method === "POST").map((c) => c.path)).toEqual(["/api/projects/p1/new-repos/preview"]);
  });

  it("reserves with the exact previewed hash and then waits in the shared proposal confirmation", async () => {
    const api = stubApi(server());
    await renderWidget(Harness);
    await previewIt();
    fireEvent.click(screen.getByRole("button", { name: "Reserve and review" }));
    await screen.findByText("Waiting for you");

    const reserve = api.calls.find((c) => c.method === "POST" && c.path === "/api/projects/p1/new-repos");
    expect(reserve?.body).toMatchObject({ leaf: "shop-docs", hash: specHash });
    // The one execute path is the proposal review; this dialog has no button that confirms by itself.
    expect(screen.queryByRole("button", { name: /^(confirm|create the folder|execute|run)/i })).toBeNull();

    fireEvent.click(await screen.findByRole("button", { name: "Review and confirm" }));
    const code = await screen.findByTestId("proposal-hash");
    expect(code.textContent).toBe(proposalHash);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Confirm" }));
    await waitFor(() => {
      expect(api.calls.some((c) => c.path === "/api/proposals/pr1/confirm")).toBe(true);
    });
    expect(api.calls.find((c) => c.path === "/api/proposals/pr1/confirm")?.body).toEqual({ hash: proposalHash });
  });

  it("preview problems are shown and block reserving", async () => {
    stubApi(server({ problems: ["A folder named shop-docs already exists."] }));
    await renderWidget(Harness);
    await previewIt();
    expect(screen.getByRole("alert").textContent).toContain("A folder named shop-docs already exists.");
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Reserve and review" }).disabled).toBe(true);
  });

  it("a lost reserve reply is retried with the same request ID, never a second one", async () => {
    let n = 0;
    const api = stubApi(
      server({
        reserve: () => (++n === 1 ? { status: 503, body: { code: "unavailable", message: "Yggdrasil is busy.", hint: "Try again." } } : { body: op() }),
      }),
    );
    await renderWidget(Harness);
    await previewIt();
    fireEvent.click(screen.getByRole("button", { name: "Reserve and review" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Yggdrasil is busy.");
    expect(screen.getByText(/may or may not have been reserved/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByText("Waiting for you");
    const posts = api.calls.filter((c) => c.method === "POST" && c.path === "/api/projects/p1/new-repos");
    expect(posts).toHaveLength(2);
    expect((posts[1]?.body as { requestId: string }).requestId).toBe((posts[0]?.body as { requestId: string }).requestId);
  });

  it("an empty folder name or a path cannot be previewed", async () => {
    stubApi(server());
    await renderWidget(Harness);
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Preview" }).disabled).toBe(true);
    typeLeaf("a/b");
    expect(screen.getByRole("alert").textContent).toContain("single folder name");
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Preview" }).disabled).toBe(true);
  });
});

describe("new repo: lifecycle wording", () => {
  const openEarlier = async (o: NewRepoOperationDTO) => {
    stubApi(server({ operation: o, list: [o] }));
    await renderWidget(Harness);
    fireEvent.click(await screen.findByRole("button", { name: "Open shop-docs" }));
  };

  it("an unknown outcome says a remote may or may not exist and is not shown as a failure or a success", async () => {
    await openEarlier(op({ state: "uncertain", stages: [{ name: "create_remote", status: "unknown" }], canRetry: false, reason: "The host did not answer." }));
    await waitFor(() => {
      expect(screen.getByRole("status").textContent).toBe("Outcome unknown");
    });
    expect(screen.getByText(/may or may not exist/)).toBeTruthy();
    expect(screen.queryByText("Stopped")).toBeNull();
    expect(screen.queryByText("Published")).toBeNull();
    expect(document.querySelector("[data-stage-status=unknown]")?.textContent).toContain("Outcome unknown");
  });

  it("a definite failure says it stopped and that nothing was deleted", async () => {
    await openEarlier(op({ state: "stopped", stages: [{ name: "write_readme", status: "failed" }], reason: "That file is locked." }));
    expect(await screen.findByText("Stopped")).toBeTruthy();
    expect(screen.getByText(/nothing was deleted/)).toBeTruthy();
    expect(screen.queryByText("Outcome unknown")).toBeNull();
  });

  it("shows progress as named steps with a text status", async () => {
    await openEarlier(
      op({
        state: "initializing",
        stages: [
          { name: "create_directory", status: "done" },
          { name: "init_git", status: "current" },
          { name: "write_readme", status: "pending" },
        ],
      }),
    );
    const list = await screen.findByRole("list", { name: "Progress" });
    expect(list.textContent).toBe("Create the folderDoneStart gitIn progressWrite the READMEWaiting");
  });

  it("a published repo shows its credential-free remote", async () => {
    await openEarlier(op({ state: "published", remoteUrl: "https://git.acme.dev/acme/shop-docs" }));
    const a = await screen.findByRole("link", { name: "https://git.acme.dev/acme/shop-docs" });
    expect(a.getAttribute("href")).toBe("https://git.acme.dev/acme/shop-docs");
  });
});

describe("new repo: explicit retry", () => {
  const stoppedOp = (over: Partial<NewRepoOperationDTO> = {}) =>
    op({ state: "stopped", stages: [{ name: "write_readme", status: "failed" }], reason: "That file is locked.", canRetry: true, ...over });
  const openRetry = async (o: NewRepoOperationDTO) => {
    const api = stubApi(server({ operation: o, list: [o] }));
    await renderWidget(Harness);
    fireEvent.click(await screen.findByRole("button", { name: "Open shop-docs" }));
    fireEvent.click(await screen.findByRole("button", { name: "Retry…" }));
    return api;
  };

  it("after a reload Retry shows the server's spec hash and sends exactly it, never by itself", async () => {
    // A reload: the page holds no in-session preview (the memory is empty, or holds another hash).
    useSpecMemory.getState().remember("o1", { kind: "init", hash: "b".repeat(64), lines: ["An older preview"] });
    const api = await openRetry(stoppedOp({ specHash }));
    const dlg = await screen.findByRole("alertdialog");
    expect(within(dlg).getByTestId("spec-hash").textContent).toBe(specHash);
    expect(api.calls.some((c) => c.path.endsWith("/retry"))).toBe(false);

    fireEvent.click(within(dlg).getByRole("button", { name: "Confirm retry" }));
    await waitFor(() => {
      expect(api.calls.some((c) => c.path === "/api/new-repos/o1/retry")).toBe(true);
    });
    expect(api.calls.filter((c) => c.path.endsWith("/retry"))).toHaveLength(1);
    expect(api.calls.find((c) => c.path === "/api/new-repos/o1/retry")?.body).toEqual({ hash: specHash });
  });

  it("uses the session's wording only when it holds the same hash", async () => {
    useSpecMemory.getState().remember("o1", { kind: "init", hash: specHash, lines: ["Write README.md"] });
    await openRetry(stoppedOp({ specHash }));
    const dlg = await screen.findByRole("alertdialog");
    expect(within(dlg).getByRole("list", { name: "What will run again" }).textContent).toContain("Write README.md");
  });

  it("without a spec hash from the server it explains and sends nothing", async () => {
    // Even a remembered preview is not trusted when the server named no hash.
    useSpecMemory.getState().remember("o1", { kind: "init", hash: specHash, lines: ["Write README.md"] });
    const api = await openRetry(stoppedOp());
    expect((await screen.findByRole("alertdialog")).textContent).toContain("no longer holds the confirmation code");
    expect(screen.queryByRole("button", { name: "Confirm retry" })).toBeNull();
    expect(api.calls.some((c) => c.path.endsWith("/retry"))).toBe(false);
  });

  it("a refused retry shows the server's reason and keeps the dialog", async () => {
    const stopped = op({ state: "stopped", stages: [{ name: "write_readme", status: "failed" }], canRetry: true, specHash });
    stubApi((c) =>
      c.path === "/api/new-repos/o1/retry"
        ? { status: 409, body: { code: "conflict", message: "The folder changed since the preview.", hint: "Start again." } }
        : server({ operation: stopped })(c),
    );
    useSpecMemory.getState().remember("o1", { kind: "init", hash: specHash, lines: ["Write README.md"] });
    await renderWidget(Harness);
    await previewIt();
    fireEvent.click(screen.getByRole("button", { name: "Reserve and review" }));
    fireEvent.click(await screen.findByRole("button", { name: "Retry…" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm retry" }));
    expect((await screen.findAllByRole("alert")).map((a) => a.textContent).join(" ")).toContain("The folder changed since the preview.");
  });
});

describe("publish", () => {
  const ready = op({ state: "ready" });
  const open = async (s: Script) => {
    stubApi(server({ operation: ready, list: [ready], ...s }));
    await renderWidget(Harness);
    fireEvent.click(await screen.findByRole("button", { name: "Open shop-docs" }));
    expect(await screen.findByText(/no remote yet/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Publish…" }));
  };

  it("with no host configured says there is nowhere to publish, not an error", async () => {
    await open({ hosts: [] });
    const g = await screen.findByText(/nowhere to publish/);
    expect(g.getAttribute("data-empty")).toBe("NoRemote");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("with a host but no token explains the missing credential", async () => {
    await open({ hosts: [{ id: "git.acme.dev", kind: "gitlab", base: "https://git.acme.dev", tokenConfigured: false }] });
    const g = await screen.findByText(/has no token/);
    expect(g.getAttribute("data-empty")).toBe("NoCredentials");
  });

  it("previews destination and visibility (private by default), then creates a proposal with the exact hash", async () => {
    const api = stubApi(server({ operation: ready, list: [ready], hosts: [{ id: "git.acme.dev", kind: "gitlab", base: "https://git.acme.dev", tokenConfigured: true }] }));
    await renderWidget(Harness);
    fireEvent.click(await screen.findByRole("button", { name: "Open shop-docs" }));
    fireEvent.click(await screen.findByRole("button", { name: "Publish…" }));
    fireEvent.change(await screen.findByLabelText(/Owner \(account/), { target: { value: "acme" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByRole("list", { name: "What will be created" });
    expect(screen.getByText("Private")).toBeTruthy();
    expect(screen.getByTestId("spec-hash").textContent).toBe(publishHash);
    expect(api.calls.find((c) => c.path.endsWith("/publish/preview"))?.body).toMatchObject({
      hostInstance: "git.acme.dev",
      owner: "acme",
      name: "shop-docs",
      visibility: "private",
    });
    expect(api.calls.some((c) => c.method === "POST" && c.path === "/api/new-repos/o1/publish")).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Create publish proposal" }));
    expect(await screen.findByRole("button", { name: "Review and confirm" })).toBeTruthy();
    expect(api.calls.find((c) => c.method === "POST" && c.path === "/api/new-repos/o1/publish")?.body).toMatchObject({ hash: publishHash, visibility: "private" });
    // The remote is only made by confirming that proposal: this panel itself has no such button.
    fireEvent.click(screen.getByRole("button", { name: "Review and confirm" }));
    expect((await screen.findByTestId("proposal-hash")).textContent).toBe("e".repeat(64));
  });

  it("a public choice is explicit and shown in the preview", async () => {
    stubApi(server({ operation: ready, list: [ready], hosts: [{ id: "git.acme.dev", kind: "gitlab", base: "https://git.acme.dev", tokenConfigured: true }] }));
    await renderWidget(Harness);
    fireEvent.click(await screen.findByRole("button", { name: "Open shop-docs" }));
    fireEvent.click(await screen.findByRole("button", { name: "Publish…" }));
    fireEvent.change(await screen.findByLabelText(/Owner \(account/), { target: { value: "acme" } });
    fireEvent.click(screen.getByLabelText(/Public: anyone/));
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Public (anyone can see it)")).toBeTruthy();
  });

  describe("after a reload", () => {
    const publishProposal = (over: Partial<ProposalDTO> = {}) =>
      proposal({ id: "pr2", kind: "publish", actionHash: "e".repeat(64), lines: ["Create acme/shop-docs"], createdAt: "2026-01-02T10:00:00Z", ...over });
    const reopen = async (o: NewRepoOperationDTO, setupProposals: ProposalDTO[]) => {
      const api = stubApi(server({ operation: o, list: [o], setupProposals }));
      await renderWidget(Harness);
      fireEvent.click(await screen.findByRole("button", { name: "Open shop-docs" }));
      return api;
    };

    it("finds a pending publish proposal again and shows it in the shared confirmation", async () => {
      const api = await reopen(ready, [proposal({ status: "executed" }), publishProposal()]);
      fireEvent.click(await screen.findByRole("button", { name: "Review and confirm" }));
      expect((await screen.findByTestId("proposal-hash")).textContent).toBe("e".repeat(64));
      expect(screen.queryByRole("button", { name: "Publish…" })).toBeNull();
      fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Confirm" }));
      await waitFor(() => {
        expect(api.calls.some((c) => c.path === "/api/proposals/pr2/confirm")).toBe(true);
      });
      expect(api.calls.find((c) => c.path === "/api/proposals/pr2/confirm")?.body).toEqual({ hash: "e".repeat(64) });
    });

    it("offers Publish again when the only publish proposal was rejected", async () => {
      await reopen(ready, [publishProposal({ status: "rejected" })]);
      expect(await screen.findByRole("button", { name: "Publish…" })).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Review and confirm" })).toBeNull();
    });

    it("keeps the proposal in view while the repo is being published", async () => {
      await reopen(op({ state: "publishing", stages: [{ name: "create_remote", status: "current" }] }), [publishProposal({ status: "confirmed" })]);
      expect(await screen.findByText("The publish proposal:")).toBeTruthy();
      fireEvent.click(await screen.findByRole("button", { name: "Review and confirm" }));
      expect((await screen.findByTestId("proposal-hash")).textContent).toBe("e".repeat(64));
    });
  });
});
