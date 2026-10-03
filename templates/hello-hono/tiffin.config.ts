import { defineConfig } from "tiffin-sdk";

export default defineConfig({
  project: "hello",
  apps: {
    api: { framework: "hono", healthcheck: "/healthz" },
  },
});
