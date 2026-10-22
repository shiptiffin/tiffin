import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "hello",
  sleepAfter: "24h", // unused for a day: its apps stop, and the next request wakes them
  apps: {
    api: { framework: "hono", healthcheck: "/healthz" },
  },
});
