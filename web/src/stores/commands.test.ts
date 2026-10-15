import { beforeEach, describe, expect, it, vi } from "vitest";
import { registerCommands, useCommandStore } from "./commands";

const ids = () => Object.keys(useCommandStore.getState().commands);

describe("command registry", () => {
  beforeEach(() => {
    useCommandStore.setState({ commands: {}, open: false });
  });

  it("registers commands from several callers and removes only its own", () => {
    const offA = registerCommands([{ id: "a", title: "A", run: vi.fn() }]);
    registerCommands([{ id: "b", title: "B", run: vi.fn() }]);
    expect(ids()).toEqual(["a", "b"]);

    offA();
    expect(ids()).toEqual(["b"]);
  });

  it("replaces a command with the same id, and a stale unregister keeps the replacement", () => {
    const offOld = registerCommands([{ id: "a", title: "Old", run: vi.fn() }]);
    registerCommands([{ id: "a", title: "New", run: vi.fn() }]);

    offOld();
    expect(useCommandStore.getState().commands.a?.title).toBe("New");
  });
});
