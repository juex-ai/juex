import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // The Go dev server runs on :8080. Proxy API + SSE during HMR.
      "/api": "http://127.0.0.1:8080",
      "/sessions/.+/events": {
        target: "http://127.0.0.1:8080",
        changeOrigin: true,
      },
      // SPA-served paths like /sessions/:id are handled by the React
      // router, NOT proxied.
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
});
