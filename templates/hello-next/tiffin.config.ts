import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "hello-next",
  apps: {
    // Two instances share one cache in Valkey: revalidating on one
    // refreshes both.
    web: { framework: "next", instances: 2, memoryMB: 512, healthcheck: "/api/health" },
  },
});
