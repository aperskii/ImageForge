import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The Go API this app talks to. Override with IMAGEFORGE_API_URL when the API
// is not on its default port.
const apiTarget = process.env.IMAGEFORGE_API_URL ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    // The app calls /uploads and /jobs as same-origin paths, so the browser
    // never makes a cross-origin request and CORS never enters the picture in
    // development. In production these are served behind one hostname.
    proxy: {
      "/uploads": { target: apiTarget, changeOrigin: true },
      "/auth": { target: apiTarget, changeOrigin: true },
      "/jobs": { target: apiTarget, changeOrigin: true },
      "/healthz": { target: apiTarget, changeOrigin: true },
      "/readyz": { target: apiTarget, changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
