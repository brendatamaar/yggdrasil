/** What the dock needs from a terminal emulator; xterm.js in the app, a fake in tests. */
export interface TermHandle {
  open: (el: HTMLElement) => void;
  write: (data: Uint8Array) => void;
  /** Keystrokes. Returns an unsubscribe function. */
  onData: (cb: (data: string) => void) => () => void;
  onResize: (cb: (size: { cols: number; rows: number }) => void) => () => void;
  fit: () => void;
  size: () => { cols: number; rows: number };
  focus: () => void;
  dispose: () => void;
}

export interface TermFactoryOptions {
  /** True for the dock's toggle shortcut, which must not reach the shell. */
  isToggleKey: (e: KeyboardEvent) => boolean;
}

export type TermFactory = (opts: TermFactoryOptions) => TermHandle | Promise<TermHandle>;
