import * as Tooltip from "@radix-ui/react-tooltip";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { createQueryClient } from "./api/client";
import { getWSClient, type WSClient } from "./api/ws";
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

  useEffect(() => syncTheme(), []);

  useEffect(() => {
    const ws = socket ?? getWSClient();
    const stop = ws.onStatus((status) => {
      useConnectionStore.setState({ status });
    });
    ws.connect();
    return () => {
      stop();
      ws.close();
    };
  }, [socket]);

  return (
    <QueryClientProvider client={queryClient}>
      <Tooltip.Provider delayDuration={300}>
        <RouterProvider router={router} />
      </Tooltip.Provider>
    </QueryClientProvider>
  );
}
