export default {
  project: "defaults",
  apps: { jobs: { role: "worker" } },
  services: { auth: {}, email: {}, analytics: {} },
  crons: { tick: { schedule: "@hourly", app: "jobs" } },
};
