export default {
  project: "ranges",
  apps: {
    a: { instances: 0, memoryMB: 32 },
    b: { instances: 17, memoryMB: 9000 },
    c: { instances: 1.5, framework: "rails", role: "daemon" },
    d: { healthcheck: "health", path: "", routes: ["bad route"] },
  },
  services: { valkey: { maxMemoryMB: 0 }, postgres: { extensions: ["Vector"] } },
};
