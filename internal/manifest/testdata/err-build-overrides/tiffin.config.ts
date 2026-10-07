export default {
  project: "builds",
  apps: {
    a: { framework: "static", builder: "dockerfile" },
    b: { framework: "next", builder: "static" },
    c: { dockerfile: "Dockerfile.dev", target: "dev" },
    d: { builder: "dockerfile", dockerfile: "../Dockerfile", install: "bun install", build: "bun run build", runtime: "node", packages: ["ffmpeg"] },
    e: { builder: "prebuilt", git: { repo: "acme/e" }, output: "dist" },
    f: { framework: "hono", output: "dist", build: "  " },
    g: { framework: "static", output: "../www" },
    h: { git: { repo: "acme/h" }, watch: ["apps/../secret", "!"] },
  },
};
