import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { createAppRouter } from "./router";
import { applyTheme, useThemeStore } from "./stores/theme";
import "./index.css";

// Apply the stored theme before the first paint to avoid a flash.
applyTheme(useThemeStore.getState().theme);

const container = document.getElementById("root");
if (!container) throw new Error("missing #root element in index.html");

createRoot(container).render(
  <StrictMode>
    <App router={createAppRouter()} />
  </StrictMode>,
);
