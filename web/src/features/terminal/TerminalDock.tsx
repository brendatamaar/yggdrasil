import { useEffect, useRef, useState } from "react";
import { createSession, type Session, type TermSocket } from "./session";
import { shouldToggle, isToggleKey } from "./shortcut";
import { useTerminalStore, type TerminalTarget } from "./store";
import type { TermFactory, TermHandle } from "./types";
import type { TermState } from "./core";

const defaultFactory: TermFactory = async (opts) => (await import("./xtermHandle")).createXterm(opts);

interface DockProps {
  /** Tests inject a fake emulator; the app loads xterm.js. */
  createTerm?: TermFactory;
  /** Tests inject a fake socket. */
  openSocket?: (url: string) => TermSocket;
}

const stateText = (s: TermState): string => {
  switch (s.tag) {
    case "idle":
    case "requesting":
      return "Starting the terminal…";
    case "connecting":
      return "Connecting…";
    case "open":
      return "Connected";
    case "ended":
      return "The terminal session ended.";
    case "failed":
      return s.message;
  }
};

/** Listens for Ctrl+` and shows the dock when it is open. Mounted once, in the app layout. */
export function TerminalDock(props: DockProps) {
  const open = useTerminalStore((s) => s.open);
  const target = useTerminalStore((s) => s.target);
  const toggle = useTerminalStore((s) => s.toggle);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!shouldToggle(e)) return;
      e.preventDefault();
      toggle();
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
    };
  }, [toggle]);

  if (!open) return null;
  // A new target is a new session: keyed so the old socket closes and a new token is requested.
  return <Dock key={target ? `${target.kind}:${target.id}` : "none"} target={target} {...props} />;
}

function Dock({ target, createTerm = defaultFactory, openSocket }: DockProps & { target: TerminalTarget | null }) {
  const setOpen = useTerminalStore((s) => s.setOpen);
  const host = useRef<HTMLDivElement>(null);
  const session = useRef<Session | null>(null);
  const [state, setState] = useState<TermState>({ tag: "idle" });

  useEffect(() => {
    if (!target) return;
    const el = host.current;
    if (!el) return;
    const returnFocusTo = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    let cancelled = false;
    let term: TermHandle | undefined;
    let stops: (() => void)[] = [];

    void Promise.resolve(createTerm({ isToggleKey })).then((t) => {
      if (cancelled) {
        t.dispose();
        return;
      }
      term = t;
      t.open(el);
      t.fit();
      const s = createSession({
        target: target.kind === "repo" ? { repoId: target.id } : { worktreeId: target.id },
        openSocket,
        onState: setState,
        onOutput: (d) => {
          t.write(d);
        },
        size: () => t.size(),
      });
      session.current = s;
      stops = [
        t.onData((d) => {
          s.sendInput(d);
        }),
        t.onResize(({ cols, rows }) => {
          s.resize(cols, rows);
        }),
      ];
      const ro = new ResizeObserver(() => {
        t.fit();
      });
      ro.observe(el);
      stops.push(() => {
        ro.disconnect();
      });
      t.focus();
      s.dispatch({ tag: "connect" });
    });

    return () => {
      cancelled = true;
      for (const stop of stops) stop();
      session.current?.dispose();
      session.current = null;
      term?.dispose();
      // Closing the dock hands the keyboard back to where it was.
      returnFocusTo?.focus();
    };
  }, [target, createTerm, openSocket]);

  const canReconnect = state.tag === "failed" || state.tag === "ended";
  return (
    <section
      aria-label="Terminal"
      className="sticky bottom-0 z-30 flex h-72 flex-col border-t border-line bg-canvas"
    >
      <header className="flex items-center gap-3 border-b border-line px-3 py-1 text-sm">
        <h2 className="font-medium">Terminal</h2>
        {target ? <span className="text-muted">{target.label}</span> : null}
        <span
          role={state.tag === "failed" ? "alert" : "status"}
          className={state.tag === "failed" ? "text-danger" : "text-muted"}
        >
          {target ? stateText(state) : "Open a repo or a task first. The terminal opens in its folder."}
        </span>
        <span className="ml-auto flex gap-2">
          {canReconnect ? (
            <button
              type="button"
              className="rounded border border-line px-2 py-0.5 hover:bg-surface"
              onClick={() => {
                session.current?.dispatch({ tag: "connect" });
              }}
            >
              Reconnect
            </button>
          ) : null}
          <button
            type="button"
            className="rounded border border-line px-2 py-0.5 hover:bg-surface"
            onClick={() => {
              setOpen(false);
            }}
          >
            Close terminal
          </button>
        </span>
      </header>
      <div ref={host} className="min-h-0 flex-1 p-1" />
    </section>
  );
}

/** A button for a page header that shows or hides the dock. */
export function TerminalToggle() {
  const open = useTerminalStore((s) => s.open);
  const toggle = useTerminalStore((s) => s.toggle);
  return (
    <button
      type="button"
      aria-pressed={open}
      aria-keyshortcuts="Control+`"
      title="Terminal (Ctrl+`)"
      className="rounded border border-line px-2 py-0.5 text-sm hover:bg-surface"
      onClick={toggle}
    >
      Terminal
    </button>
  );
}
