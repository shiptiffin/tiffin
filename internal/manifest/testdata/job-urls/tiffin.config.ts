export default {
  project: "hooks",
  crons: {
    digest: { schedule: "0 9 * * 1-5", url: "https://hooks.example.com/digest", timezone: "Europe/London", timeoutSeconds: 30 },
  },
  queues: {
    orders: { url: "https://hooks.example.com/orders", concurrency: 4 },
  },
  topics: { "order.created": { subscribers: ["orders"] } },
};
