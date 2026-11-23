/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  // The development server sends the API requests to the panel API in api/
  // (`php artisan demo:serve`, docs/development.md).
  server: {
    // The API gets the host of the browser ("localhost"): a passkey works only on its own
    // domain (FR-103), and the brand of the sign-in page comes from the domain.
    proxy: { "/api": { target: "http://127.0.0.1:8000", changeOrigin: false } },
  },
  test: {
    environment: "jsdom",
  },
});
