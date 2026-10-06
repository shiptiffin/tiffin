export default {
  project: "rel",
  apps: {
    site: { framework: "static", release: "bun run migrate.ts" },
    blank: { release: "  " },
  },
  services: { postgres: { previews: "shared" } },
};
