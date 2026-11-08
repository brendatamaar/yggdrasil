import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { WSClient } from "../api/ws";
import { WSContext } from "../api/wsContext";
import { FakeSocket } from "./fakeSocket";

/** Renders `ui` with a query client and a hub client on a fake socket; `push` delivers a topic message. */
export function renderWithWS(ui: ReactNode) {
  const ws = new WSClient({ socketCtor: FakeSocket });
  ws.connect();
  const socket = FakeSocket.last();
  socket.open();
  const seqs = new Map<string, number>();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const result = render(
    <QueryClientProvider client={client}>
      <WSContext.Provider value={ws}>{ui}</WSContext.Provider>
    </QueryClientProvider>,
  );
  const push = (topic: string, data: unknown) => {
    const seq = (seqs.get(topic) ?? 0) + 1;
    seqs.set(topic, seq);
    socket.receive({ type: "event", topic, seq, data });
  };
  return Object.assign(result, { push, client, socket });
}
