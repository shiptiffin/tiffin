export default {
  project: "cmds",
  apps: {
    site: { framework: "static", command: "bun run serve.ts" },
    blank: { role: "worker", command: "  " },
  },
};
