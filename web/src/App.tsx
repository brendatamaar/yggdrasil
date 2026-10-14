import * as Tooltip from "@radix-ui/react-tooltip";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import type { AppRouter } from "./router";
import { syncTheme } from "./stores/theme";

interface AppProps {
  router: AppRouter;
}

export function App({ router }: AppProps) {
  const [queryClient] = useState(() => new QueryClient());

  useEffect(() => syncTheme(), []);

  return (
    <QueryClientProvider client={queryClient}>
      <Tooltip.Provider delayDuration={300}>
        <RouterProvider router={router} />
      </Tooltip.Provider>
    </QueryClientProvider>
  );
}
