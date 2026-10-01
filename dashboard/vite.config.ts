import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// In dev, forward API calls to a local pgmux admin API.
const admin = process.env.PGMUX_ADMIN ?? "http://127.0.0.1:9930";

export default defineConfig({
  plugins: [react()],
  base: "./",
  server: {
    proxy: Object.fromEntries(["/events", "/stats", "/pools"].map((p) => [p, admin])),
  },
});
