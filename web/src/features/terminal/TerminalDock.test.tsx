import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { stubApi } from "../../test/fakeApi";
import type { TermSocket } from "./session";
import { shouldToggle } from "./shortcut";
import { useTerminalStore } from "./store";
import { TerminalDock } from "./TerminalDock";
import type { TermFactory, TermHandle } from "./types";

class Sock implements TermSocket {
  binaryType: BinaryType = "blob";
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  sent: unknown[] = [];
  closed = false;
  constructor(readonly url: string) {}
  send(d: string | Uint8Array<ArrayBuffer>) {
    this.sent.push(d);
  }
  close() {
    this.closed = true;
  }
}

function fakeTerm() {
  const written: Uint8Array[] = [];
  const term: TermHandle = {
    open: (el) => {
      el.setAttribute("data-term", "1");
    },
    write: (d) => {
      written.push(d);
    },
    onData: () => () => undefined,
    onResize: () => () => undefined,
    fit: () => undefined,
    size: () => ({ cols: 80, rows: 24 }),
    focus: () => undefined,
    dispose: () => undefined,
  };
  const factory: TermFactory = () => term;
  return { factory, written };
}

function setup(tokenReply?: () => { status?: number; body: unknown }) {
  let n = 0;
  const api = stubApi((c) => {
    if (c.path === "/api/terminal") return tokenReply ? tokenReply() : { body: { token: `token-${String(++n)}-SECRET`, expiresIn: 30 } };
    return undefined;
  });
  const sockets: Sock[] = [];
  const t = fakeTerm();
  const openSocket = (u: string) => {
    const s = new Sock(u);
    sockets.push(s);
    return s;
  };
  render(<TerminalDock createTerm={t.factory} openSocket={openSocket} />);
  return { api, sockets, ...t };
}

const open = (target: "repo" | "worktree" = "repo") => {
  act(() => {
    useTerminalStore.setState({ open: true, target: { kind: target, id: "r1", label: "shop-api" } });
  });
};
const ctrlBacktick = (el: Element | Window = window) => fireEvent.keyDown(el, { key: "`", code: "Backquote", ctrlKey: true });

afterEach(() => {
  useTerminalStore.setState({ open: false, target: null });
});

describe("TerminalDock", () => {
  it("is hidden until opened, and Ctrl+` toggles it", async () => {
    setup();
    expect(screen.queryByRole("region", { name: "Terminal" })).toBeNull();
    useTerminalStore.setState({ target: { kind: "repo", id: "r1", label: "shop-api" } });
    ctrlBacktick();
    expect(await screen.findByRole("region", { name: "Terminal" })).toBeTruthy();
    ctrlBacktick();
    await waitFor(() => { expect(screen.queryByRole("region", { name: "Terminal" })).toBeNull(); });
  });

  it("keeps the key for a text field outside the terminal", () => {
    setup();
    render(<input aria-label="Name" />);
    ctrlBacktick(screen.getByLabelText("Name"));
    expect(useTerminalStore.getState().open).toBe(false);
  });

  it("asks for a token for the repo, connects, and never puts the token in the page", async () => {
    const h = setup();
    open();
    await waitFor(() => { expect(h.sockets).toHaveLength(1); });
    const call = h.api.calls.find((c) => c.path === "/api/terminal");
    expect(call?.method).toBe("POST");
    expect(call?.body).toEqual({ repoId: "r1" });
    act(() => h.sockets[0]?.onopen?.());
    expect(await screen.findByText("Connected")).toBeTruthy();
    expect(document.body.innerHTML).not.toContain("SECRET");
    expect(document.body.textContent).not.toContain("token-1");
  });

  it("opens a worktree target with the worktree ID", async () => {
    const h = setup();
    open("worktree");
    await waitFor(() => { expect(h.sockets).toHaveLength(1); });
    expect(h.api.calls.find((c) => c.path === "/api/terminal")?.body).toEqual({ worktreeId: "r1" });
  });

  it("reconnect asks for a new token", async () => {
    const h = setup();
    open();
    await waitFor(() => { expect(h.sockets).toHaveLength(1); });
    act(() => h.sockets[0]?.onopen?.());
    act(() => h.sockets[0]?.onclose?.());
    fireEvent.click(await screen.findByRole("button", { name: "Reconnect" }));
    await waitFor(() => { expect(h.sockets).toHaveLength(2); });
    expect(h.api.calls.filter((c) => c.path === "/api/terminal")).toHaveLength(2);
    expect(h.sockets[1]?.url).toContain("token-2-SECRET");
  });

  it("closing the dock closes the socket", async () => {
    const h = setup();
    open();
    await waitFor(() => { expect(h.sockets).toHaveLength(1); });
    fireEvent.click(screen.getByRole("button", { name: "Close terminal" }));
    expect(h.sockets[0]?.closed).toBe(true);
    expect(screen.queryByRole("region", { name: "Terminal" })).toBeNull();
  });

  it("explains too many sessions and an untracked folder", async () => {
    setup(() => ({ status: 429, body: { code: "too_many", message: "No." } }));
    open();
    expect((await screen.findByRole("alert")).textContent).toContain("Too many terminals are open");
  });

  it("explains a folder that is no longer tracked", async () => {
    setup(() => ({ status: 404, body: { code: "not_found", message: "Gone." } }));
    open();
    expect((await screen.findByRole("alert")).textContent).toContain("no longer tracked");
  });

  it("says what to do when the page offers no folder", () => {
    setup();
    act(() => {
      useTerminalStore.setState({ open: true, target: null });
    });
    expect(screen.getByText(/Open a repo or a task first/)).toBeTruthy();
  });

  it("has keyboard-reachable buttons", async () => {
    setup();
    open();
    const close = await screen.findByRole("button", { name: "Close terminal" });
    expect(close.tabIndex).toBe(0);
  });
});

describe("shouldToggle", () => {
  const ev = (init: KeyboardEventInit, target?: Element) => {
    const e = new KeyboardEvent("keydown", { bubbles: true, ...init });
    if (target) Object.defineProperty(e, "target", { value: target });
    return e;
  };
  it("needs Ctrl and the backtick key", () => {
    expect(shouldToggle(ev({ key: "`", ctrlKey: true }))).toBe(true);
    expect(shouldToggle(ev({ key: "`" }))).toBe(false);
    expect(shouldToggle(ev({ key: "a", ctrlKey: true }))).toBe(false);
    expect(shouldToggle(ev({ key: "`", ctrlKey: true, altKey: true }))).toBe(false);
  });
  it("is off in text fields, on in the terminal's own textarea", () => {
    const area = document.createElement("textarea");
    expect(shouldToggle(ev({ key: "`", ctrlKey: true }, area))).toBe(false);
    const wrap = document.createElement("div");
    wrap.className = "xterm";
    const inner = document.createElement("textarea");
    wrap.appendChild(inner);
    expect(shouldToggle(ev({ key: "`", ctrlKey: true }, inner))).toBe(true);
  });
});
