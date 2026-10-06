import { defineConfig } from "tiffin-sdk";

export default defineConfig({
  project: "guestbook",
  resources: { memoryMB: 768, cpus: 1.5, maxSharePercent: 25 },
  sleepAfter: "7d",
  apps: { web: {}, jobs: { role: "worker", memoryMB: 256 } },
});
