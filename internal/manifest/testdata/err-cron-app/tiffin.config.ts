export default {
  project: "ghostcron",
  apps: { web: {}, jobs: { role: "worker" } },
  crons: {
    ghost: { schedule: "0 * * * *", app: "missing" },
    fine: { schedule: "0 * * * *", app: "jobs" },
  },
};
