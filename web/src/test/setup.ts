import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// jsdom doesn't implement scrolling; the router's scroll handling calls it.
window.scrollTo = () => undefined;

// cmdk (command palette) needs these; jsdom has neither.
globalThis.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};
Element.prototype.scrollIntoView = () => undefined;

// Vitest globals are off, so Testing Library can't register this itself.
afterEach(() => {
  cleanup();
  localStorage.clear();
});
