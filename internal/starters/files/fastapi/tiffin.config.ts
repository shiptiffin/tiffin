import { defineConfig } from "@shiptiffin/sdk";

// An API in Python: FastAPI with its own Postgres database. Alembic migrates
// it once per deploy (release), before the new version takes traffic.
export default defineConfig({
  project: "py-notes",
  apps: {
    api: { framework: "fastapi", healthcheck: "/healthz", release: "alembic upgrade head" },
  },
  services: {
    postgres: {},
  },
});
