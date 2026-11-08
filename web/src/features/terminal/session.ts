import { ApiError, describeError } from "../../api/client";
import type { TerminalTokenRequest, TerminalTokenResponse } from "../../api/gen/api.gen";
import { requestTerminalToken, resizeFrame, terminalSocketUrl } from "../../api/terminal";
import {
  classifyTokenFailure,
  failureMessages,
  initialTermState,
  termStep,
  type TermCommand,
  type TermInput,
  type TermState,
} from "./core";

/** The part of WebSocket the session uses; tests inject a fake. */
export interface TermSocket {
  binaryType: BinaryType;
  onopen: (() => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: (() => void) | null;
  onerror: (() => void) | null;
  send(data: string | Uint8Array<ArrayBuffer>): void;
  close(): void;
}

export interface SessionDeps {
  target: TerminalTokenRequest;
  /** Defaults to POST /api/terminal. */
  requestToken?: (target: TerminalTokenRequest) => Promise<TerminalTokenResponse>;
  /** Defaults to the browser WebSocket. */
  openSocket?: (url: string) => TermSocket;
  onState: (state: TermState) => void;
  /** Bytes from the shell. */
  onOutput: (data: Uint8Array) => void;
  size: () => { cols: number; rows: number };
}

export interface Session {
  dispatch: (input: TermInput) => void;
  /** Keystrokes from the terminal; dropped unless the socket is open. */
  sendInput: (data: string) => void;
  resize: (cols: number, rows: number) => void;
  getState: () => TermState;
  /** Ends the session and closes the socket. */
  dispose: () => void;
}

const encoder = new TextEncoder();

/** Runs the terminal reducer: dispatches inputs, performs the commands it returns. */
export function createSession(deps: SessionDeps): Session {
  const requestToken = deps.requestToken ?? requestTerminalToken;
  const openSocket = deps.openSocket ?? ((url: string) => new WebSocket(url) as TermSocket);
  let state = initialTermState;
  let socket: TermSocket | null = null;
  let disposed = false;

  const run = (cmd: TermCommand) => {
    switch (cmd.tag) {
      case "requestToken":
        requestToken(deps.target).then(
          (r) => {
            dispatch({ tag: "tokenIssued", token: r.token });
          },
          (e: unknown) => {
            // A message from the server is already plain language; keep it for the cases we don't name.
            const kind = e instanceof ApiError ? classifyTokenFailure(e.status, e.code) : "other";
            const message = kind === "other" ? describeError(e) : failureMessages[kind];
            dispatch({ tag: "tokenRejected", kind, message });
          },
        );
        return;
      case "openSocket": {
        const s = openSocket(terminalSocketUrl(cmd.token));
        s.binaryType = "arraybuffer";
        socket = s;
        s.onopen = () => {
          if (socket === s) dispatch({ tag: "socketOpened" });
        };
        s.onmessage = (ev) => {
          if (socket === s && ev.data instanceof ArrayBuffer) deps.onOutput(new Uint8Array(ev.data));
        };
        s.onclose = () => {
          if (socket !== s) return;
          socket = null;
          dispatch({ tag: "socketClosed" });
        };
        s.onerror = () => {
          if (socket === s) dispatch({ tag: "socketFailed" });
        };
        return;
      }
      case "sendSize": {
        const { cols, rows } = deps.size();
        socket?.send(resizeFrame(cols, rows));
        return;
      }
      case "closeSocket": {
        const s = socket;
        socket = null;
        if (s) {
          s.onopen = s.onmessage = s.onclose = s.onerror = null;
          s.close();
        }
        return;
      }
    }
  };

  function dispatch(input: TermInput): void {
    if (disposed) return;
    const next = termStep(state, input);
    state = next.state;
    for (const c of next.commands) run(c);
    deps.onState(state);
  }

  return {
    dispatch,
    sendInput(data) {
      if (state.tag === "open") socket?.send(encoder.encode(data));
    },
    resize(cols, rows) {
      if (state.tag === "open") socket?.send(resizeFrame(cols, rows));
    },
    getState: () => state,
    dispose() {
      if (disposed) return;
      dispatch({ tag: "close" });
      disposed = true;
    },
  };
}
