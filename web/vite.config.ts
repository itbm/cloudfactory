/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  // The development server sends the API requests to the panel API in api/
  // (`php artisan demo:serve`, docs/development.md), and the WebSocket of the web terminal to
  // the terminal relay (`go run ./cmd/cf-terminal-relay` in agent/).
  server: {
    // The API gets the host of the browser ("localhost"): a passkey works only on its own
    // domain (FR-103), and the brand of the sign-in page comes from the domain.
    proxy: {
      "/api/terminal": { target: "http://127.0.0.1:8091", ws: true },
      "/api": { target: "http://127.0.0.1:8000", changeOrigin: false },
    },
  },
  test: {
    environment: "jsdom",
    // Each test file runs in its own VM context in a shared process. A new process for each
    // file made the tests 2.5 times slower. The files still do not share modules or a DOM.
    pool: "vmForks",
  },
});
