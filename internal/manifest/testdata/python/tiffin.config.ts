export default {
  project: "pyshop",
  apps: {
    api: { framework: "fastapi", healthcheck: "/healthz", release: "alembic upgrade head" },
    admin: { framework: "python", path: "admin", command: "gunicorn --bind 0.0.0.0:$PORT shop.wsgi" },
  },
  services: { postgres: {} },
};
