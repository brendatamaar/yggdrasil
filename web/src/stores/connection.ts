import { create } from "zustand";
import type { ConnectionStatus } from "../api/ws";

// UI-only state: mirrors the WebSocket connection for the status pill.

export const useConnectionStore = create<{ status: ConnectionStatus }>()(() => ({ status: "closed" }));
