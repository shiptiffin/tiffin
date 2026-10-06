export default {
  project: "notes",
  apps: {
    web: { framework: "next", release: "bunx drizzle-kit migrate" },
  },
  services: { postgres: { previews: "branch" } },
};
