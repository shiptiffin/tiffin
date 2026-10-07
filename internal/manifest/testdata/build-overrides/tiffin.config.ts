export default {
  project: "builds",
  apps: {
    web: {
      framework: "next",
      install: "pnpm install --frozen-lockfile",
      build: "pnpm --filter web build",
      command: "node server.js",
      output: "./out/",
      builder: "auto",
      healthcheck: "/api/health",
      release: "pnpm db:migrate",
      watch: [" apps/web/** ", "packages/ui", "apps/web/**", "!**/*.md"],
      git: { repo: "acme/mono", path: "apps/web" },
    },
    api: { builder: "dockerfile", dockerfile: "./docker/api.Dockerfile", target: "runner", command: "./api --port $PORT" },
    plain: { builder: "dockerfile", dockerfile: "Dockerfile" },
    site: { builder: "static", output: "dist" },
    image: { builder: "prebuilt", command: "bun run start" },
  },
};
