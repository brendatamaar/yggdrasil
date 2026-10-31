import * as Tooltip from "@radix-ui/react-tooltip";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { apiFetch, createQueryClient } from "./api/client";
import { SessionRequiredPage, useSessionStore } from "./features/session";
import { getWSClient, type WSClient } from "./api/ws";
import { WSContext } from "./api/wsContext";
import type { AppRouter } from "./router";
import { useConnectionStore } from "./stores/connection";
import { syncTheme } from "./stores/theme";

interface AppProps {
  router: AppRouter;
  /** Tests pass a client with a fake socket. */
  socket?: WSClient;
}

export function App({ router, socket }: AppProps) {
  const [queryClient] = useState(createQueryClient);
  const ws = socket ?? getWSClient();
  const sessionRequired = useSessionStore((s) => s.state.tag === "required");

  useEffect(() => syncTheme(), []);

  useEffect(() => {
    let opened = false;
    let probed = false;
    const stop = ws.onStatus((status) => {
      useConnectionStore.setState({ status });
      if (status === "open") opened = true;
      // A browser hides why an upgrade failed. If the socket never opened, ask the API once: a 401
      // session_required there shows the session page. One probe, no loop.
      if (status === "reconnecting" && !opened && !probed) {
        probed = true;
        apiFetch("/api/agents").catch(() => undefined);
      }
    });
    ws.connect();
    return () => {
      stop();
      ws.close();
    };
  }, [ws]);

  return (
    <QueryClientProvider client={queryClient}>
      <WSContext.Provider value={ws}>
        <Tooltip.Provider delayDuration={300}>
          {sessionRequired ? <SessionRequiredPage /> : <RouterProvider router={router} />}
        </Tooltip.Provider>
      </WSContext.Provider>
    </QueryClientProvider>
  );
}
