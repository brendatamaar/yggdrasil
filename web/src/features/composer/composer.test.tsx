import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory } from "@tanstack/react-router";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../../App";
import { WSClient } from "../../api/ws";
import { createAppRouter } from "../../router";
import { stubApi, type Handler } from "../../test/fakeApi";
import { FakeSocket } from "../../test/fakeSocket";
import { DraftButton } from "./DraftButton";
import { MessageComposer } from "./ModeSwitch";
import { useComposerUi } from "./store";

const counts = { all: 1, uncommitted: 0, behind: 0, ahead: 0, failedPipeline: 0, problems: 0 };
const projects = [{ id: "prj_1", name: "acme-shop", path: "x", counts, repos: [{ id: "rep_1", name: "shop-api", flags: [], conflicted: 0, ahead: 0, behind: 0, staged: 0, unstaged: 0, untracked: 0, failedPipeline: false }] }];
const created = { task: { id: "tsk_9", title: "T", status: "running", repoIds: [], mode: "ask" } };

function base(h?: Handler): Handler {
  return (c) => {
    const own = h?.(c);
    if (own) return own;
    if (c.path.startsWith("/api/repos?")) return { body: { projects, counts } };
    if (c.path.startsWith("/api/composer/models")) return { body: { agent: "mock", models: [] } };
    if (c.path === "/api/composer/prompts" && c.method === "GET") return { body: { prompts: [] } };
    if (c.method === "POST" && c.path === "/api/tasks") return { status: 201, body: created };
    if (c.path === "/api/tasks/tsk_9") return { body: created };
    return undefined;
  };
}

function renderHome(h?: Handler) {
  const api = stubApi(base(h));
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
  render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
  return api;
}

const promptBox = () => screen.getByLabelText("What should the agent look into?");
const start = () => screen.getByRole<HTMLButtonElement>("button", { name: "Start task" });
const posts = (api: ReturnType<typeof stubApi>) => api.calls.filter((c) => c.method === "POST" && c.path === "/api/tasks");

async function ready(text = "Do it") {
  await screen.findByRole("option", { name: "acme-shop" }, { timeout: 8000 });
  fireEvent.change(promptBox(), { target: { value: text } });
}

beforeEach(() => {
  FakeSocket.instances = [];
  localStorage.clear();
  useComposerUi.setState({ writeModesOff: false, acknowledged: [] });
});

describe("modes", () => {
  it("shows only Ask when write modes are off", async () => {
    useComposerUi.setState({ writeModesOff: true });
    renderHome();
    await ready();
    expect(screen.getAllByRole("radio").map((r) => (r as HTMLInputElement).value)).toEqual(["ask"]);
  });

  it("hides the write modes after the server refuses one", async () => {
    renderHome((c) =>
      c.method === "POST" && c.path === "/api/tasks"
        ? { status: 400, body: { code: "bad_request", message: "Code mode is not enabled in this version.", hint: "Use Ask." } }
        : undefined,
    );
    await ready();
    expect(screen.getAllByRole("radio")).toHaveLength(4);
    fireEvent.click(screen.getByRole("radio", { name: "Code" }));
    fireEvent.click(start());
    await screen.findByRole("alert");
    await waitFor(() => {
      expect(screen.getAllByRole("radio")).toHaveLength(1);
    });
  });

  it("warns before Current branch and sends optIn only after confirming", async () => {
    const api = renderHome();
    await ready();
    fireEvent.click(screen.getByRole("radio", { name: "Current branch" }));
    fireEvent.click(start());
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("edits your main checkout directly");
    expect(dialog.textContent).toContain("Only one editor per checkout");
    expect(posts(api)).toHaveLength(0);

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(posts(api)).toHaveLength(0);

    fireEvent.click(start());
    fireEvent.click(await screen.findByRole("button", { name: "I understand, start the task" }));
    await screen.findByRole("heading", { name: "T" });
    expect(posts(api)[0]?.body).toMatchObject({ mode: "current_branch", optIn: true, scope: "repo", repoIds: ["rep_1"] });
  });

  it("Whole project warns, sends project scope with no repos", async () => {
    const api = renderHome();
    await ready();
    fireEvent.click(screen.getByRole("radio", { name: "Whole project" }));
    fireEvent.click(start());
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("every tracked repo");
    fireEvent.click(within(dialog).getByRole("button", { name: "I understand, start the task" }));
    await screen.findByRole("heading", { name: "T" });
    expect(posts(api)[0]?.body).toMatchObject({ mode: "project", scope: "project", repoIds: [], optIn: true });
  });

  it("does not send optIn for Ask or Code", async () => {
    const api = renderHome();
    await ready();
    fireEvent.click(screen.getByRole("radio", { name: "Code" }));
    fireEvent.click(start());
    await screen.findByRole("heading", { name: "T" });
    expect(posts(api)[0]?.body).not.toHaveProperty("optIn");
  });
});

function withQuery(ui: React.ReactElement) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>;
}

describe("mode per message", () => {
  it("offers only Ask and the task's write mode, and sends the chosen mode", async () => {
    const api = stubApi();
    render(withQuery(<MessageComposer taskId="t1" currentMode="ask" writeMode="code" />));
    expect(screen.getAllByRole("radio").map((r) => r.parentElement?.textContent)).toEqual(["Ask", "Code"]);
    fireEvent.click(screen.getByRole("radio", { name: "Code" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Next message" }), { target: { value: "go" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => {
      expect(api.calls.find((c) => c.path === "/api/tasks/t1/messages")?.body).toEqual({ mode: "code", prompt: "go" });
    });
  });

  it("offers no switch for an Ask task without a write mode", () => {
    stubApi();
    render(withQuery(<MessageComposer taskId="t1" currentMode="ask" />));
    expect(screen.queryAllByRole("radio")).toHaveLength(0);
  });

  it("a Current-branch task can only go back to Ask", () => {
    stubApi();
    render(withQuery(<MessageComposer taskId="t1" currentMode="current_branch" writeMode="current_branch" />));
    expect(screen.getAllByRole("radio").map((r) => r.parentElement?.textContent)).toEqual(["Current branch", "Ask"]);
  });
});

describe("model", () => {
  it("validates a custom model name", async () => {
    const api = renderHome();
    await ready();
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "__custom__" } });
    fireEvent.change(screen.getByLabelText("Custom model name"), { target: { value: "bad name!" } });
    expect(start().disabled).toBe(true);
    expect(screen.getByText(/Model names use letters/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Custom model name"), { target: { value: "acme-model-1.5:beta" } });
    expect(start().disabled).toBe(false);
    fireEvent.click(start());
    await screen.findByRole("heading", { name: "T" });
    expect(posts(api)[0]?.body).toMatchObject({ model: "acme-model-1.5:beta" });
  });
});

describe("keyboard", () => {
  it("Ctrl+Enter in the prompt starts the task", async () => {
    const api = renderHome();
    await ready();
    fireEvent.keyDown(promptBox(), { key: "Enter", ctrlKey: true });
    await screen.findByRole("heading", { name: "T" });
    expect(posts(api)).toHaveLength(1);
  });
});

describe("saved prompts", () => {
  it("lists, inserts, saves, renames and deletes", async () => {
    let list = [{ id: "sp1", name: "Review cart", text: "Review the cart code" }];
    let n = 1;
    const api = renderHome((c) => {
      if (c.path === "/api/composer/prompts" && c.method === "GET") return { body: { prompts: list } };
      if (c.path === "/api/composer/prompts" && c.method === "POST") {
        const b = c.body as { name: string; text: string };
        n += 1;
        const p = { id: `sp${String(n)}`, ...b };
        list = [...list, p];
        return { status: 201, body: p };
      }
      if (c.method === "DELETE") {
        list = list.filter((p) => `/api/composer/prompts/${p.id}` !== c.path);
        return { body: {} };
      }
      return undefined;
    });
    await ready("");
    fireEvent.click(screen.getByRole("button", { name: "Saved prompts" }));
    fireEvent.click(await screen.findByRole("button", { name: "Review cart" }));
    expect(promptBox()).toHaveProperty("value", "Review the cart code");

    // Save the current text.
    fireEvent.click(screen.getByRole("button", { name: "Saved prompts" }));
    fireEvent.change(await screen.findByLabelText("Name for the current text"), { target: { value: "Cart again" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("button", { name: "Cart again" });
    expect(api.calls.find((c) => c.method === "POST" && c.path === "/api/composer/prompts")?.body).toEqual({
      name: "Cart again",
      text: "Review the cart code",
    });

    // Rename.
    fireEvent.click(screen.getByRole("button", { name: "Rename Cart again" }));
    fireEvent.change(screen.getByLabelText("New name for Cart again"), { target: { value: "Cart v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save name" }));
    await screen.findByRole("button", { name: "Cart v2" });
    expect(screen.queryByRole("button", { name: "Cart again" })).toBeNull();

    // Delete asks first and removes only that prompt.
    fireEvent.click(screen.getByRole("button", { name: "Delete Cart v2" }));
    expect(api.calls.some((c) => c.method === "DELETE" && c.path.endsWith("/sp3"))).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Keep" }));
    expect(screen.getByRole("button", { name: "Cart v2" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Delete Cart v2" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Cart v2" })).toBeNull();
    });
    expect(screen.getByRole("button", { name: "Review cart" })).toBeTruthy();
  });

  it("renders names as plain text", async () => {
    renderHome((c) =>
      c.path === "/api/composer/prompts" && c.method === "GET"
        ? { body: { prompts: [{ id: "x", name: "<img src=x onerror=alert(1)>", text: "t" }] } }
        : undefined,
    );
    await ready();
    fireEvent.click(screen.getByRole("button", { name: "Saved prompts" }));
    expect((await screen.findByRole("button", { name: "<img src=x onerror=alert(1)>" })).textContent).toBe("<img src=x onerror=alert(1)>");
    expect(document.querySelector("img")).toBeNull();
  });
});

describe("@ mentions", () => {
  const mentions = {
    mentions: [
      { repoId: "rep_1", repoName: "shop-api", path: "src/Cart.java", kind: "file" },
      { repoId: "rep_1", repoName: "shop-api", path: "<b>evil</b>", kind: "dir" },
    ],
    truncated: false,
  };

  it("opens a combobox, moves with arrows, inserts @repo/path and closes on Escape", async () => {
    const api = renderHome((c) => (c.path.startsWith("/api/composer/mentions") ? { body: mentions } : undefined));
    await ready("");
    const box = promptBox() as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "Look at @sh", selectionStart: 11 } });
    box.setSelectionRange(11, 11);
    fireEvent.select(box);
    const list = await screen.findByRole("listbox");
    const options = await within(list).findAllByRole("option");
    expect(box.getAttribute("aria-expanded")).toBe("true");
    expect(box.getAttribute("aria-activedescendant")).toBe(options[0]?.id);
    expect(document.querySelector("b")).toBeNull();
    await waitFor(() => {
      expect(api.calls.some((c) => c.path === "/api/composer/mentions?project=prj_1&q=sh")).toBe(true);
    });
    expect(posts(api)).toHaveLength(0);
    await waitFor(() => {
      expect(screen.queryByText("Searching...")).toBeNull();
    });

    fireEvent.keyDown(box, { key: "ArrowDown" });
    expect(box.getAttribute("aria-activedescendant")).toBe(options[1]?.id);
    fireEvent.keyDown(box, { key: "ArrowUp" });
    fireEvent.keyDown(box, { key: "Enter" });
    expect(box).toHaveProperty("value", "Look at @shop-api/src/Cart.java ");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(posts(api)).toHaveLength(0);

    // Escape closes without inserting.
    fireEvent.change(box, { target: { value: "x @c", selectionStart: 4 } });
    box.setSelectionRange(4, 4);
    fireEvent.select(box);
    await screen.findByRole("listbox");
    fireEvent.keyDown(box, { key: "Escape" });
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(box).toHaveProperty("value", "x @c");
  });

  it("aborts the stale request when the query changes", async () => {
    const signals = new Map<string, AbortSignal>();
    const handler = base();
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const path = typeof input === "string" ? input : "";
        if (path.includes("/api/composer/mentions")) {
          signals.set(path, init?.signal as AbortSignal);
          return new Promise<Response>((_, reject) => {
            init?.signal?.addEventListener("abort", () => {
              reject(new DOMException("aborted", "AbortError"));
            });
          });
        }
        const r = handler({ method: init?.method ?? "GET", path }) ?? { status: 404, body: {} };
        return Promise.resolve(new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { "Content-Type": "application/json" } }));
      }),
    );
    const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
    render(<App router={router} socket={new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket })} />);
    await ready("");
    const box = promptBox() as HTMLTextAreaElement;
    const type = (v: string) => {
      fireEvent.change(box, { target: { value: v, selectionStart: v.length } });
      box.setSelectionRange(v.length, v.length);
      fireEvent.select(box);
    };
    type("@a");
    await waitFor(() => {
      expect(signals.has("/api/composer/mentions?project=prj_1&q=a")).toBe(true);
    });
    type("@ab");
    await waitFor(() => {
      expect(signals.has("/api/composer/mentions?project=prj_1&q=ab")).toBe(true);
    });
    expect(signals.get("/api/composer/mentions?project=prj_1&q=a")?.aborted).toBe(true);
    expect(signals.get("/api/composer/mentions?project=prj_1&q=ab")?.aborted).toBe(false);
  });
});

describe("DraftButton", () => {
  it("loads, can be cancelled, then fills the field and nothing else", async () => {
    let resolveDraft: ((r: Response) => void) | undefined;
    const seen: string[] = [];
    let aborted = false;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        seen.push(`${init?.method ?? "GET"} ${typeof input === "string" ? input : ""}`);
        return new Promise<Response>((resolve, reject) => {
          resolveDraft = resolve;
          init?.signal?.addEventListener("abort", () => {
            aborted = true;
            reject(new DOMException("aborted", "AbortError"));
          });
        });
      }),
    );
    const onDraft = vi.fn();
    render(<DraftButton kind="commit_message" projectId="prj_1" repoId="rep_1" onDraft={onDraft} />);
    fireEvent.click(screen.getByRole("button", { name: "Draft commit message" }));
    expect(screen.getByRole("status").textContent).toContain("Drafting");

    fireEvent.click(screen.getByRole("button", { name: "Cancel draft" }));
    expect(aborted).toBe(true);
    expect(screen.getByRole("button", { name: "Draft commit message" })).toBeTruthy();
    expect(onDraft).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Draft commit message" }));
    await act(async () => {
      resolveDraft?.(new Response(JSON.stringify({ text: "fix(cart): round totals" }), { status: 200, headers: { "Content-Type": "application/json" } }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(onDraft).toHaveBeenCalledWith("fix(cart): round totals");
    });
    expect(seen.every((s) => s === "POST /api/composer/drafts")).toBe(true);
  });

  it("shows plain error text and offers a retry", async () => {
    stubApi((c) =>
      c.path === "/api/composer/drafts"
        ? { status: 400, body: { code: "bad_request", message: "<b>No diff to describe.</b>", hint: "Make a change first." } }
        : undefined,
    );
    const onDraft = vi.fn();
    render(<DraftButton kind="mr_description" projectId="prj_1" repoId="rep_1" onDraft={onDraft} />);
    fireEvent.click(screen.getByRole("button", { name: "Draft MR description" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("<b>No diff to describe.</b>");
    expect(document.querySelector("b")).toBeNull();
    expect(onDraft).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Draft MR description" })).toBeTruthy();
  });
});
