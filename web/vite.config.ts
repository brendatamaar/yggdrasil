import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

// The Go server (cmd/ygg) that the dev server proxies to.
const backend = "http://127.0.0.1:4747";

// emptyOutDir wipes dist/, including the committed dist/.gitkeep that lets
// web/embed.go compile before the frontend is built. Emit it again as part of
// the bundle so a build leaves the working tree clean.
function keepGitkeep(): Plugin {
  return {
    name: "ygg:keep-gitkeep",
    apply: "build",
    generateBundle() {
      this.emitFile({ type: "asset", fileName: ".gitkeep", source: "" });
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), keepGitkeep()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": { target: backend },
      "/ws": { target: backend, ws: true },
    },
  },
  preview: {
    host: "127.0.0.1",
    strictPort: true,
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  test: {
    environment: "jsdom",
    // The default "forks" pool timed out starting its workers on Windows
    // ("Timeout waiting for worker to respond"); threads start reliably.
    pool: "threads",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["src/test/setup.ts"],
  },
});
