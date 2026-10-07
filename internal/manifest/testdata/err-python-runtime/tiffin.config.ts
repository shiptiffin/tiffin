export default {
  project: "pyrt",
  apps: {
    api: { framework: "fastapi", runtime: "node" },
    site: { framework: "python", runtime: "bun" },
  },
};
