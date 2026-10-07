// The box's server for a React Router (framework mode) build on Bun:
// Bun.serve with React Router's own request handler, in place of
// react-router-serve (Express and compression), which is slow on Bun. Run
// in the app's folder, with the server build as its argument:
//
//   bun /app/.tiffin/react-router/serve.js build/server/index.js
//
// It serves the build's client files itself (the box's edge serves most of
// them first), prerendered pages included, and stops gracefully on SIGTERM.
import { readdirSync } from "node:fs";
import { join, posix, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const cwd = process.cwd();
const buildPath = resolve(cwd, process.argv[2] || "build/server/index.js");
const build = await import(pathToFileURL(buildPath).href);

let handle;
if (typeof build.default?.fetch === "function") {
  handle = (req) => build.default.fetch(req); // an RSC build
} else {
  // The app's own react-router, wherever its workspace keeps it.
  const { createRequestHandler } = await import(Bun.resolveSync("react-router", cwd));
  handle = createRequestHandler(build, process.env.NODE_ENV || "production");
}

let base = "/";
try {
  base = new URL(build.publicPath || "/", "http://x").pathname;
} catch {}
const client = resolve(cwd, build.assetsBuildDirectory || join(buildPath, "..", "..", "client"));
const assets = posix.join(base, "assets") + "/";

// Client files by URL path, read once. A prerendered page
// (about/index.html) answers /about and /about/ as well.
const files = new Map();
function walk(dir, rel) {
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return;
  }
  for (const e of entries) {
    if (e.name.startsWith(".") && !(rel === "" && e.name === ".well-known")) continue;
    const p = rel + "/" + e.name;
    if (e.isDirectory()) walk(join(dir, e.name), p);
    else if (e.isFile()) {
      const url = posix.join(base, p);
      files.set(url, join(dir, e.name));
      if (e.name === "index.html") {
        const page = posix.join(base, rel) || "/";
        files.set(page, join(dir, e.name));
        if (page !== "/") files.set(page + "/", join(dir, e.name));
      }
    }
  }
}
walk(client, "");

// The edge ends TLS and says so in X-Forwarded-Proto (and X-Forwarded-Host),
// headers only the box sets: the request React Router sees gets the URL the
// browser used, which its action origin check (CSRF) compares with Origin.
function publicRequest(req) {
  const proto = req.headers.get("x-forwarded-proto")?.split(",")[0].trim();
  const host = req.headers.get("x-forwarded-host")?.split(",")[0].trim();
  if (!proto && !host) return req;
  const url = new URL(req.url);
  if (proto === "https" || proto === "http") url.protocol = proto;
  if (host) url.host = host;
  return url.href === req.url ? req : new Request(url, req);
}

const server = Bun.serve({
  port: Number(process.env.PORT) || 3000,
  hostname: process.env.HOST || undefined,
  // The box's switchboard keeps connections open for reuse; it closes them.
  idleTimeout: 0,
  async fetch(req) {
    if (req.method === "GET" || req.method === "HEAD") {
      let path = new URL(req.url).pathname;
      try {
        path = decodeURIComponent(path);
      } catch {}
      const file = files.get(path);
      if (file) {
        const headers = { "Cache-Control": path.startsWith(assets) ? "public, max-age=31536000, immutable" : "public, max-age=0, must-revalidate" };
        return new Response(Bun.file(file), { headers });
      }
    }
    return handle(publicRequest(req));
  },
  error(err) {
    console.error(err);
    return new Response("Internal Server Error", { status: 500 });
  },
});
console.log(`React Router on Bun ${Bun.version}: http://localhost:${server.port} (${files.size} client files)`);

// SIGTERM: stop taking connections and let requests in flight finish, for
// up to SHUTDOWN_TIMEOUT seconds (25 by default, inside the box's 30).
let stopping = false;
for (const sig of ["SIGTERM", "SIGINT"]) {
  process.on(sig, async () => {
    if (stopping) process.exit(1);
    stopping = true;
    const limit = Number(process.env.SHUTDOWN_TIMEOUT) || 25;
    const timer = setTimeout(() => server.stop(true).then(() => process.exit(0)), limit * 1000);
    await server.stop();
    clearTimeout(timer);
    process.exit(0);
  });
}
