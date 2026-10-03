import { defineConfig } from "tiffin-sdk";

export default defineConfig({
  project: "jobs",
  apps: {
    // A worker has no public routes: the box pushes jobs, cron ticks and
    // workflow turns to it over the box's internal network.
    worker: { role: "worker" },
  },
  crons: {
    tick: { schedule: "* * * * *", app: "worker", path: "/cron/tick" },
  },
});
