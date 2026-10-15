import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FakeSocket } from "../test/fakeSocket";
import { WSClient } from "./ws";

function setup() {
  const client = new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket, baseDelayMs: 100, maxDelayMs: 400 });
  const onEvent = vi.fn();
  const onGap = vi.fn();
  return { client, onEvent, onGap };
}

describe("WSClient", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeSocket.instances = [];
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("subscribes after open and unsubscribes with the last handler", () => {
    const { client, onEvent, onGap } = setup();
    const off = client.subscribe("task:1", { onEvent, onGap });
    client.connect();
    FakeSocket.last().open();
    expect(FakeSocket.last().sent).toEqual([{ type: "subscribe", topic: "task:1" }]);

    off();
    expect(FakeSocket.last().sent.at(-1)).toEqual({ type: "unsubscribe", topic: "task:1" });
  });

  it("reconnects with exponential backoff, resubscribes and signals a gap", () => {
    const { client, onEvent, onGap } = setup();
    client.subscribe("task:1", { onEvent, onGap });
    client.connect();
    FakeSocket.last().open();
    expect(onGap).not.toHaveBeenCalled();

    FakeSocket.last().drop();
    expect(client.status).toBe("reconnecting");
    vi.advanceTimersByTime(99);
    expect(FakeSocket.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeSocket.instances).toHaveLength(2);

    // Second failure waits twice as long.
    FakeSocket.last().drop();
    vi.advanceTimersByTime(199);
    expect(FakeSocket.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeSocket.instances).toHaveLength(3);

    FakeSocket.last().open();
    expect(client.status).toBe("open");
    expect(FakeSocket.last().sent).toEqual([{ type: "subscribe", topic: "task:1" }]);
    expect(onGap).toHaveBeenCalledTimes(1);
  });

  it("caps the delay and resets it after a successful open", () => {
    const { client } = setup();
    client.connect();
    for (const wait of [100, 200, 400, 400]) {
      FakeSocket.last().drop();
      const before = FakeSocket.instances.length;
      vi.advanceTimersByTime(wait);
      expect(FakeSocket.instances).toHaveLength(before + 1);
    }
    FakeSocket.last().open();
    FakeSocket.last().drop();
    vi.advanceTimersByTime(100);
    expect(client.status).toBe("reconnecting");
    expect(FakeSocket.instances).toHaveLength(6);
  });

  it("does not reconnect after close()", () => {
    const { client } = setup();
    client.connect();
    FakeSocket.last().open();
    client.close();
    vi.advanceTimersByTime(10_000);
    expect(FakeSocket.instances).toHaveLength(1);
    expect(client.status).toBe("closed");
  });

  it("signals a gap when seq jumps, and not for consecutive events", () => {
    const { client, onEvent, onGap } = setup();
    client.subscribe("task:1", { onEvent, onGap });
    client.connect();
    const s = FakeSocket.last();
    s.open();

    s.receive({ type: "event", topic: "task:1", seq: 1 });
    s.receive({ type: "event", topic: "task:1", seq: 2 });
    expect(onGap).not.toHaveBeenCalled();

    s.receive({ type: "event", topic: "task:1", seq: 5 });
    expect(onGap).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledTimes(3);

    s.receive({ type: "event", topic: "task:1", seq: 6 });
    expect(onGap).toHaveBeenCalledTimes(1);
  });

  it("tracks seq per topic", () => {
    const { client, onEvent, onGap } = setup();
    client.subscribe("task:1", { onEvent, onGap });
    client.subscribe("task:2", { onEvent, onGap });
    client.connect();
    const s = FakeSocket.last();
    s.open();

    s.receive({ type: "event", topic: "task:1", seq: 10 });
    s.receive({ type: "event", topic: "task:2", seq: 3 });
    s.receive({ type: "event", topic: "task:1", seq: 11 });
    s.receive({ type: "event", topic: "task:2", seq: 4 });
    expect(onGap).not.toHaveBeenCalled();
  });

  it("forwards a server gap message and restarts counting", () => {
    const { client, onEvent, onGap } = setup();
    client.subscribe("task:1", { onEvent, onGap });
    client.connect();
    const s = FakeSocket.last();
    s.open();

    s.receive({ type: "event", topic: "task:1", seq: 1 });
    s.receive({ type: "gap", topic: "task:1" });
    expect(onGap).toHaveBeenCalledTimes(1);
    s.receive({ type: "event", topic: "task:1", seq: 40 });
    expect(onGap).toHaveBeenCalledTimes(1);
  });
});
