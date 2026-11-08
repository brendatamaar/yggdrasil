import { describe, expect, it } from "vitest";
import {
  classifyTokenFailure,
  initialTermState,
  termStep,
  type TermInput,
  type TermState,
} from "./core";

const SECRET = "tok-SECRET-123";
const run = (state: TermState, ...inputs: TermInput[]) => inputs.reduce((s, i) => termStep(s, i).state, state);

describe("termStep", () => {
  it("walks idle -> requesting -> connecting -> open", () => {
    let r = termStep(initialTermState, { tag: "connect" });
    expect(r.state.tag).toBe("requesting");
    expect(r.commands).toEqual([{ tag: "requestToken" }]);
    r = termStep(r.state, { tag: "tokenIssued", token: SECRET });
    expect(r.state.tag).toBe("connecting");
    expect(r.commands).toEqual([{ tag: "openSocket", token: SECRET }]);
    r = termStep(r.state, { tag: "socketOpened" });
    expect(r.state.tag).toBe("open");
    expect(r.commands).toEqual([{ tag: "sendSize" }]);
  });

  it("asks for a new token on every connect after a failure or an end", () => {
    for (const from of [
      { tag: "failed", kind: "other", message: "x" } as const,
      { tag: "ended" } as const,
    ]) {
      expect(termStep(from, { tag: "connect" }).commands).toEqual([{ tag: "requestToken" }]);
    }
  });

  it("ignores a second connect while one is in progress or open", () => {
    for (const s of [{ tag: "requesting" }, { tag: "connecting" }, { tag: "open" }] as const) {
      expect(termStep(s, { tag: "connect" })).toEqual({ state: s, commands: [] });
    }
  });

  it("drops a token that arrives after the dock was closed", () => {
    const closed = run(initialTermState, { tag: "connect" }, { tag: "close" });
    expect(termStep(closed, { tag: "tokenIssued", token: SECRET })).toEqual({ state: closed, commands: [] });
  });

  it("a socket closed before opening means the link expired; after opening it means the shell ended", () => {
    expect(run({ tag: "connecting" }, { tag: "socketClosed" })).toMatchObject({ tag: "failed", kind: "expired" });
    expect(run({ tag: "open" }, { tag: "socketClosed" })).toEqual({ tag: "ended" });
  });

  it("closing from any live state closes the socket", () => {
    for (const s of [{ tag: "requesting" }, { tag: "connecting" }, { tag: "open" }, { tag: "ended" }] as const) {
      expect(termStep(s, { tag: "close" })).toEqual({ state: { tag: "idle" }, commands: [{ tag: "closeSocket" }] });
    }
    expect(termStep({ tag: "idle" }, { tag: "close" }).commands).toEqual([]);
  });

  it("never keeps the token in its state, over random input sequences", () => {
    const pool: TermInput[] = [
      { tag: "connect" },
      { tag: "tokenIssued", token: SECRET },
      { tag: "tokenRejected", kind: "too_many", message: "m" },
      { tag: "socketOpened" },
      { tag: "socketClosed" },
      { tag: "socketFailed" },
      { tag: "close" },
    ];
    let seed = 7;
    const rnd = () => (seed = (seed * 1103515245 + 12345) % 2147483648);
    for (let run = 0; run < 200; run++) {
      let s: TermState = initialTermState;
      for (let i = 0; i < 25; i++) {
        const input = pool[rnd() % pool.length] as TermInput;
        const r = termStep(s, input);
        s = r.state;
        expect(JSON.stringify(s)).not.toContain(SECRET);
        // A socket is only opened right after a token was issued while requesting.
        if (r.commands.some((c) => c.tag === "openSocket")) expect(s.tag).toBe("connecting");
      }
    }
  });
});

describe("classifyTokenFailure", () => {
  it("names the cases the dock explains", () => {
    expect(classifyTokenFailure(404)).toBe("not_tracked");
    expect(classifyTokenFailure(429)).toBe("too_many");
    expect(classifyTokenFailure(410)).toBe("expired");
    expect(classifyTokenFailure(503)).toBe("shutting_down");
    // Only those four statuses are named; 409 and 403 are no longer used for these.
    for (const s of [400, 401, 403, 409, 500]) expect(classifyTokenFailure(s)).toBe("other");
  });
});
