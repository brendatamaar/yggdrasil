import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { parseTaskStatus } from "../api/taskStatus";
import { WSClient } from "../api/ws";
import { WSContext } from "../api/wsContext";
import { FakeSocket } from "../test/fakeSocket";
import { LiveSync } from "./LiveSync";

function setup() {
  const ws = new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket, baseDelayMs: 1 });
  const queryClient = new QueryClient();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
  ws.connect();
  FakeSocket.last().open();
  render(
    <QueryClientProvider client={queryClient}>
      <WSContext.Provider value={ws}>
        <LiveSync />
      </WSContext.Provider>
    </QueryClientProvider>,
  );
  return { ws, invalidate, socket: FakeSocket.last() };
}

const status = (seq: number, data: unknown) => ({ type: "event", topic: "tasks", seq, data });

describe("LiveSync", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
  });

  it("subscribes to the tasks topic", () => {
    const { socket } = setup();
    expect(socket.sent).toContainEqual({ type: "subscribe", topic: "tasks" });
  });

  it("refreshes task queries on every status change and makes no notification itself", () => {
    const { socket, invalidate } = setup();
    socket.receive(status(1, { taskId: "t1", title: "Find the routes", status: "running" }));
    socket.receive(status(2, { taskId: "t1", title: "Find the routes", status: "done" }));
    expect(invalidate).toHaveBeenCalledTimes(2);
    // Notices come from the server's "notices" topic (features/notifications), not from task events.
    expect(socket.sent).not.toContainEqual({ type: "subscribe", topic: "notices" });
  });

  it("ignores messages that are not task statuses", () => {
    const { socket, invalidate } = setup();
    socket.receive(status(1, "nope"));
    socket.receive(status(2, { taskId: 1, title: "x", status: "done" }));
    socket.receive(status(3, null));
    expect(invalidate).not.toHaveBeenCalled();
  });

  it("refreshes after a gap", () => {
    const { socket, invalidate } = setup();
    socket.receive(status(1, { taskId: "t1", title: "a", status: "running" }));
    socket.receive(status(3, { taskId: "t1", title: "a", status: "running" })); // seq 2 was missed
    // one for the gap, two for the two messages
    expect(invalidate).toHaveBeenCalledTimes(3);
  });

  it("stops listening when it unmounts", () => {
    const ws = new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket });
    ws.connect();
    FakeSocket.last().open();
    const queryClient = new QueryClient();
    const { unmount } = render(
      <QueryClientProvider client={queryClient}>
        <WSContext.Provider value={ws}>
          <LiveSync />
        </WSContext.Provider>
      </QueryClientProvider>,
    );
    unmount();
    expect(FakeSocket.last().sent).toContainEqual({ type: "unsubscribe", topic: "tasks" });
  });
});

describe("parseTaskStatus", () => {
  it("reads the three fields and nothing else", () => {
    expect(parseTaskStatus({ taskId: "t", title: "T", status: "done", extra: 1 })).toEqual({ taskId: "t", title: "T", status: "done" });
    expect(parseTaskStatus({ taskId: "t", title: "T" })).toBeNull();
    expect(parseTaskStatus(undefined)).toBeNull();
  });
});
