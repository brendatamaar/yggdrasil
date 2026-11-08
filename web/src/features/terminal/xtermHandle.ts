import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import type { TermHandle } from "./types";

/** The real terminal: xterm.js with the fit addon. Loaded on demand, the first time the dock opens. */
export function createXterm(opts: { isToggleKey: (e: KeyboardEvent) => boolean }): TermHandle {
  const term = new Terminal({ cursorBlink: true, fontFamily: "ui-monospace, Consolas, monospace", fontSize: 13, scrollback: 5000 });
  const fit = new FitAddon();
  term.loadAddon(fit);
  // Keep the dock's own shortcut out of the shell: xterm ignores the key and the page handles it.
  term.attachCustomKeyEventHandler((e) => !opts.isToggleKey(e));
  return {
    open: (el) => {
      term.open(el);
    },
    write: (data) => {
      term.write(data);
    },
    onData: (cb) => {
      const d = term.onData(cb);
      return () => {
        d.dispose();
      };
    },
    onResize: (cb) => {
      const d = term.onResize(cb);
      return () => {
        d.dispose();
      };
    },
    fit: () => {
      fit.fit();
    },
    size: () => ({ cols: term.cols, rows: term.rows }),
    focus: () => {
      term.focus();
    },
    dispose: () => {
      term.dispose();
    },
  };
}
