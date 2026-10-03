export default {
  project: "badrefs",
  apps: { jobs: { role: "worker" } },
  queues: {
    ghost: { app: "missing" },
    emails: { app: "jobs" },
    orders: { app: "jobs" },
    limited: { app: "jobs", rateLimit: 5 },
  },
  topics: {
    "order.created": { subscribers: ["emails", "nope"] },
    // A name cannot be both a queue and a topic.
    emails: { subscribers: ["orders"] },
  },
};
