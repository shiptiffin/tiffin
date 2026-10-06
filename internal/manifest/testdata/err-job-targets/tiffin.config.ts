export default {
  project: "hooks",
  apps: { web: {} },
  crons: {
    neither: { schedule: "@daily" },
    both: { schedule: "@daily", app: "web", url: "https://hooks.example.com/both" },
    pathed: { schedule: "@hourly", url: "https://hooks.example.com/x", path: "/x" },
    secret: { schedule: "@hourly", url: "https://user:pw@hooks.example.com/x" },
  },
  queues: {
    nothing: {},
    hashed: { url: "https://hooks.example.com/orders#top" },
  },
};
