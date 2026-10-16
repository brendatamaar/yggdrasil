import { createContext, useContext } from "react";
import type { WSClient } from "./ws";

/** The app's WebSocket client; App provides it so tests can inject a fake socket. */
export const WSContext = createContext<WSClient | null>(null);

export function useWS(): WSClient {
  const client = useContext(WSContext);
  if (!client) throw new Error("useWS needs a WSContext provider (App provides one)");
  return client;
}
