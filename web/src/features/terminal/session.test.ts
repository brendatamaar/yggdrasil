import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../../api/client";
import type { TerminalTokenRequest } from "../../api/gen/api.gen";
import type { TermState } from "./core";
import { createSession, type TermSocket } from "./session";

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

function setup(tokens: string[] = ["t1", "t2", "t3"]) {
  const sockets: Sock[] = [];
  const states: TermState[] = [];
  const out: Uint8Array[] = [];
  const targets: TerminalTokenRequest[] = [];
  let n = 0;
  const requestToken = vi.fn((t: TerminalTokenRequest) => {
    targets.push(t);
    return Promise.resolve({ token: tokens[n++] ?? "tx", expiresIn: 30 });
  });
  const session = createSession({
    target: { repoId: "r1" },
    requestToken,
    openSocket: (url) => {
      const s = new Sock(url);
      sockets.push(s);
      return s;
    },
    onState: (s) => states.push(s),
    onOutput: (d) => out.push(d),
    size: () => ({ cols: 100, rows: 30 }),
  });
  return { session, sockets, states, out, requestToken, targets };
}
const tick = () => new Promise((r) => setTimeout(r, 0));

describe("terminal session", () => {
  it("requests a token, opens a binary socket and sends the size once open", async () => {
    const h = setup();
    h.session.dispatch({ tag: "connect" });
    await tick();
    expect(h.targets).toEqual([{ repoId: "r1" }]);
    const s = h.sockets[0] as Sock;
    expect(s.binaryType).toBe("arraybuffer");
    expect(s.url).toContain("/ws/terminal?token=t1");
    s.onopen?.();
    expect(h.session.getState().tag).toBe("open");
    expect(JSON.parse(s.sent[0] as string)).toEqual({ type: "resize", cols: 100, rows: 30 });
  });

  it("sends keystrokes as binary frames and writes binary output", async () => {
    const h = setup();
    h.session.dispatch({ tag: "connect" });
    await tick();
    const s = h.sockets[0] as Sock;
    s.onopen?.();
    h.session.sendInput("ls\r");
    expect(ArrayBuffer.isView(s.sent[1])).toBe(true); // a binary frame, not a text one
    expect(new TextDecoder().decode(s.sent[1] as Uint8Array)).toBe("ls\r");
    s.onmessage?.({ data: new Uint8Array([104, 105]).buffer });
    expect(Array.from(h.out[0] as Uint8Array)).toEqual([104, 105]);
    s.onmessage?.({ data: "text frames are ignored" });
    expect(h.out).toHaveLength(1);
  });

  it("drops keystrokes and resizes until the socket is open", async () => {
    const h = setup();
    h.session.sendInput("x");
    h.session.resize(10, 10);
    h.session.dispatch({ tag: "connect" });
    await tick();
    h.session.sendInput("x");
    expect(h.sockets[0]?.sent).toEqual([]);
  });

  it("asks for a NEW token on every reconnect", async () => {
    const h = setup();
    h.session.dispatch({ tag: "connect" });
    await tick();
    (h.sockets[0] as Sock).onopen?.();
    (h.sockets[0] as Sock).onclose?.();
    expect(h.session.getState().tag).toBe("ended");
    h.session.dispatch({ tag: "connect" });
    await tick();
    expect(h.requestToken).toHaveBeenCalledTimes(2);
    expect(h.sockets[1]?.url).toContain("token=t2");
    expect(h.sockets[1]?.url).not.toContain("token=t1");
  });

  it("explains a refused token request in plain words", async () => {
    const cases: [number, string, string][] = [
      [429, "x", "Too many terminals"],
      [404, "not_found", "no longer tracked"],
    ];
    for (const [status, code, text] of cases) {
      const h = setup();
      const s = createSession({
        target: { repoId: "r1" },
        requestToken: () => Promise.reject(new ApiError(status, { code, message: "m" })),
        onState: (st) => h.states.push(st),
        onOutput: () => undefined,
        size: () => ({ cols: 1, rows: 1 }),
      });
      s.dispatch({ tag: "connect" });
      await tick();
      const st = s.getState();
      expect(st.tag === "failed" && st.message).toContain(text);
    }
  });

  it("shows the server's own words for other failures", async () => {
    const s = createSession({
      target: { repoId: "r1" },
      requestToken: () => Promise.reject(new ApiError(500, { code: "internal", message: "Boom.", hint: "Try again." })),
      onState: () => undefined,
      onOutput: () => undefined,
      size: () => ({ cols: 1, rows: 1 }),
    });
    s.dispatch({ tag: "connect" });
    await tick();
    expect(s.getState()).toMatchObject({ tag: "failed", message: "Boom. Try again." });
  });

  it("dispose closes the socket and ignores a late token", async () => {
    const h = setup();
    h.session.dispatch({ tag: "connect" });
    await tick();
    h.session.dispose();
    expect(h.sockets[0]?.closed).toBe(true);

    const late = setup();
    late.session.dispatch({ tag: "connect" });
    late.session.dispose(); // before the token resolves
    await tick();
    expect(late.sockets).toHaveLength(0);
  });

  it("never reports the token through state callbacks", async () => {
    const h = setup(["SECRET-TOKEN"]);
    h.session.dispatch({ tag: "connect" });
    await tick();
    expect(JSON.stringify(h.states)).not.toContain("SECRET-TOKEN");
  });
});
