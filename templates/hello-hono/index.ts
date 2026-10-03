import { Hono } from "hono";
import { logger } from "hono/logger";

// A tiny Hono API on Bun. Tiffin gives every instance its own $PORT and
// collects stdout/stderr: `tiffin logs api -f`.
const app = new Hono();
const started = new Date().toISOString();

app.use(logger());

app.get("/", (c) =>
  c.json({
    hello: "world",
    app: process.env.TIFFIN_APP ?? "hello-hono",
    deploy: process.env.TIFFIN_DEPLOY ?? "local",
    message: process.env.GREETING ?? "Hello from Tiffin",
    started,
  }),
);

// The healthcheck in tiffin.config.ts points here: cheap, no dependencies.
app.get("/healthz", (c) => c.text("ok"));

const port = Number(process.env.PORT ?? 3000);
console.log(`hello-hono listening on :${port}`);

export default { port, fetch: app.fetch };
