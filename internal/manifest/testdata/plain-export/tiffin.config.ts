export default {
  project: "plain",
  apps: {
    api: { framework: "hono", routes: ["api"] },
  },
  services: { valkey: { maxMemoryMB: 128 } },
};
