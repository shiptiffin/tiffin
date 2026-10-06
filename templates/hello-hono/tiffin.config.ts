import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "hello",
  apps: {
    api: { framework: "hono", healthcheck: "/healthz" },
  },
});
