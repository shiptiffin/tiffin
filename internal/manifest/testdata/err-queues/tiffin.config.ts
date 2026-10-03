export default {
  project: "badqueues",
  apps: { jobs: { role: "worker" } },
  queues: {
    "Bad_Name": { app: "jobs" },
    noapp: {},
    ranges: { app: "jobs", path: "no-slash", concurrency: -1, keyConcurrency: 1001, rateLimit: 10001, ratePeriodSeconds: 86401, maxAttempts: 101, leaseSeconds: 4 },
    zero: { app: "jobs", maxAttempts: -1, leaseSeconds: 3601 },
    fine: { app: "jobs" },
  },
};
