import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "jobs",
  apps: {
    // A worker has no public routes: the box pushes jobs, cron ticks and
    // workflow turns to it over the box's internal network.
    worker: { role: "worker" },
  },
  // The box pushes each job sent to "work" to POST /queues/work on the worker,
  // at most 2 at a time per job key. Declared here, so `tiffin apply` keeps the
  // settings; send to it with queue.send("work", payload, { key }).
  queues: {
    work: { app: "worker", keyConcurrency: 2, leaseSeconds: 10 },
  },
  crons: {
    tick: { schedule: "* * * * *", app: "worker", path: "/cron/tick" },
  },
});
