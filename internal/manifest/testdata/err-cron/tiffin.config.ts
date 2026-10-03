export default {
  project: "badcron",
  apps: { web: {} },
  crons: {
    "Bad Name": { schedule: "* * * * *", app: "web" },
    ghost: { schedule: "0 * * * *", app: "missing" },
    short: { schedule: "* * * *", app: "web" },
    chars: { schedule: "0 0 $ * *", app: "web" },
    macro: { schedule: "@yearly", app: "web" },
    nopath: { schedule: "@daily", app: "web", path: "tick" },
    incomplete: {},
  },
};
