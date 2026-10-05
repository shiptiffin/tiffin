export default {
  project: "ghostcron",
  apps: { web: {}, jobs: { role: "worker" } },
  crons: {
    ghost: { schedule: "0 * * * *", app: "missing" },
    fine: { schedule: "0 * * * *", app: "jobs", timezone: "Europe/London" },
    zone: { schedule: "@daily", app: "jobs", timezone: "Mars/Olympus_Mons" },
    local: { schedule: "@daily", app: "jobs", timezone: "Local" },
  },
};
