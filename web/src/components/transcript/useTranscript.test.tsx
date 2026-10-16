import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { EventMessage, type Event as AgentEvent } from "../../api/gen/agents.gen";
import { WSClient } from "../../api/ws";
import { WSContext } from "../../api/wsContext";
import { FakeSocket } from "../../test/fakeSocket";
import { stubApi } from "../../test/fakeApi";
import { useTranscript } from "./useTranscript";

const ev = (seq: number): AgentEvent => ({ seq, kind: EventMessage, at: "2026-10-16T10:00:00Z", text: `event ${String(seq)}` });

function Probe({ taskId }: { taskId: string }) {
  const t = useTranscript(taskId);
  return (
    <div>
      <p data-testid="seqs">{t.events.map((e) => e.seq).join(",")}</p>
      <p data-testid="loaded">{String(t.loaded)}</p>
      {t.error ? <p role="alert">{t.error}</p> : null}
    </div>
  );
}

function setup(taskId = "t1") {
  const ws = new WSClient({ url: "ws://x/ws", socketCtor: FakeSocket, baseDelayMs: 1 });
  ws.connect();
  FakeSocket.last().open();
  const view = render(
    <WSContext.Provider value={ws}>
      <Probe taskId={taskId} />
    </WSContext.Provider>,
  );
  return { ws, socket: FakeSocket.last(), view };
}

const live = (seq: number, topic = "task:t1") => ({ type: "event", topic, seq, data: ev(seq) });
const seqs = () => screen.getByTestId("seqs").textContent;

describe("useTranscript", () => {
  beforeEach(() => {
    FakeSocket.instances = [];
  });

  it("backfills over REST and subscribes to the task topic", async () => {
    const { calls } = stubApi((c) => (c.path.startsWith("/api/tasks/t1/events") ? { body: { events: [ev(1), ev(2), ev(3)] } } : undefined));
    const { socket } = setup();
    await waitFor(() => {
      expect(seqs()).toBe("1,2,3");
    });
    expect(screen.getByTestId("loaded").textContent).toBe("true");
    expect(calls[0]?.path).toBe("/api/tasks/t1/events?after=0");
    expect(socket.sent).toContainEqual({ type: "subscribe", topic: "task:t1" });
  });

  it("appends live events without fetching again", async () => {
    const { calls } = stubApi((c) => (c.path.includes("/events") ? { body: { events: [ev(1), ev(2)] } } : undefined));
    const { socket } = setup();
    await waitFor(() => {
      expect(seqs()).toBe("1,2");
    });
    socket.receive(live(3));
    socket.receive(live(4));
    await waitFor(() => {
      expect(seqs()).toBe("1,2,3,4");
    });
    expect(calls.filter((c) => c.path.includes("/events"))).toHaveLength(1);
  });

  it("shows an event once when the backfill and the live message overlap", async () => {
    stubApi((c) => (c.path.includes("/events") ? { body: { events: [ev(1), ev(2)] } } : undefined));
    const { socket } = setup();
    socket.receive(live(2)); // arrives while the backfill is in flight
    socket.receive(live(1));
    await waitFor(() => {
      expect(seqs()).toBe("1,2");
    });
    socket.receive(live(2));
    expect(seqs()).toBe("1,2");
  });

  it("fetches the missing events when a live message is ahead of what it holds", async () => {
    let served = 0;
    const { calls } = stubApi((c) => {
      if (!c.path.includes("/events")) return undefined;
      served++;
      return { body: { events: served === 1 ? [ev(1), ev(2)] : [ev(3), ev(4), ev(5), ev(6)] } };
    });
    const { socket } = setup();
    await waitFor(() => {
      expect(seqs()).toBe("1,2");
    });
    socket.receive(live(6)); // 3 to 5 never arrived
    await waitFor(() => {
      expect(seqs()).toBe("1,2,3,4,5,6");
    });
    const eventCalls = calls.filter((c) => c.path.includes("/events"));
    expect(eventCalls[1]?.path).toBe("/api/tasks/t1/events?after=2");
  });

  it("fetches again after a reconnect", async () => {
    let served = 0;
    const { calls } = stubApi((c) => {
      if (!c.path.includes("/events")) return undefined;
      served++;
      return { body: { events: served === 1 ? [ev(1)] : [ev(2), ev(3)] } };
    });
    const { socket } = setup();
    await waitFor(() => {
      expect(seqs()).toBe("1");
    });
    socket.drop();
    await waitFor(() => {
      expect(FakeSocket.instances).toHaveLength(2);
    });
    FakeSocket.last().open();
    await waitFor(() => {
      expect(seqs()).toBe("1,2,3");
    });
    expect(calls.filter((c) => c.path.includes("/events"))[1]?.path).toBe("/api/tasks/t1/events?after=1");
  });

  it("ignores messages of other tasks and malformed data", async () => {
    stubApi((c) => (c.path.includes("/events") ? { body: { events: [ev(1)] } } : undefined));
    const { socket } = setup();
    await waitFor(() => {
      expect(seqs()).toBe("1");
    });
    socket.receive(live(2, "task:other"));
    socket.receive({ type: "event", topic: "task:t1", seq: 2, data: { nonsense: true } });
    expect(seqs()).toBe("1");
  });

  it("shows a plain-language error when the events can't be loaded", async () => {
    stubApi(() => ({ status: 500, body: { code: "internal", message: "Something went wrong inside Yggdrasil.", hint: "Try again." } }));
    setup();
    expect((await screen.findByRole("alert")).textContent).toContain("Something went wrong inside Yggdrasil.");
    expect(screen.getByTestId("loaded").textContent).toBe("true");
  });

  it("starts over for another task and unsubscribes from the first", async () => {
    stubApi((c) => {
      if (c.path.startsWith("/api/tasks/t1/events")) return { body: { events: [ev(1)] } };
      if (c.path.startsWith("/api/tasks/t2/events")) return { body: { events: [ev(1), ev(2)] } };
      return undefined;
    });
    const { socket, view, ws } = setup("t1");
    await waitFor(() => {
      expect(seqs()).toBe("1");
    });
    view.rerender(
      <WSContext.Provider value={ws}>
        <Probe taskId="t2" />
      </WSContext.Provider>,
    );
    await waitFor(() => {
      expect(seqs()).toBe("1,2");
    });
    expect(socket.sent).toContainEqual({ type: "unsubscribe", topic: "task:t1" });
    expect(socket.sent).toContainEqual({ type: "subscribe", topic: "task:t2" });
  });
});
