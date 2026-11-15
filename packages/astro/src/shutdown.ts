/**
 * Graceful shutdown for @astrojs/node's standalone server.
 *
 * The adapter's server entry starts listening when it is imported and keeps
 * the server to itself: no SIGTERM handler, and `stop()` destroys open
 * connections. It does export `startServer()` (returning the server) and skips
 * its own start when ASTRO_NODE_AUTOSTART is "disabled" (its preview command
 * relies on both). So the integration has Astro write the adapter's entry as
 * `astro-entry.mjs` (build.serverEntry) and writes `entry.mjs` itself: it
 * imports the adapter's entry with autostart off, starts the server, and on
 * SIGTERM/SIGINT stops accepting connections, lets in-flight requests finish
 * (idle keep-alive connections are closed), and exits; after `timeout` it
 * closes whatever is left. Its exports are the adapter entry's, so
 * `astro preview` and anything importing entry.mjs see the same module.
 */

export const ADAPTER_ENTRY = "astro-entry.mjs";

/** The source of the entry.mjs that wraps the adapter's entry. */
export function shutdownEntrySource(timeoutSeconds: number, adapterEntry = ADAPTER_ENTRY): string {
  const ms = Math.max(0, Math.round(timeoutSeconds * 1000));
  return `// Written by @shiptiffin/astro: @astrojs/node's standalone server with graceful shutdown.
// The adapter's own entry is ./${adapterEntry}; this file starts it and drains it on SIGTERM.
const autostart = process.env.ASTRO_NODE_AUTOSTART;
process.env.ASTRO_NODE_AUTOSTART = "disabled";
const app = await import(${JSON.stringify("./" + adapterEntry)});
if (autostart === undefined) delete process.env.ASTRO_NODE_AUTOSTART;
else process.env.ASTRO_NODE_AUTOSTART = autostart;

export const handler = app.handler;
export const startServer = app.startServer;
export const options = app.options;

if (app.options?.mode === "standalone" && autostart !== "disabled" && typeof app.startServer === "function") {
  const { server, done } = app.startServer();
  done.catch((err) => {
    console.error(err);
    process.exit(1);
  });
  const timeoutMs = ${ms};
  let stopping = false;
  const stop = (signal) => {
    if (stopping) return;
    stopping = true;
    const http = server.server;
    console.log("[@shiptiffin/astro] " + signal + ": finishing in-flight requests (up to " + timeoutMs / 1000 + " s)");
    const force = setTimeout(() => {
      console.log("[@shiptiffin/astro] shutdown timeout: closing the remaining connections");
      Promise.resolve(server.stop()).catch(() => {}).finally(() => process.exit(0));
    }, timeoutMs);
    http.close(() => {
      clearTimeout(force);
      process.exit(0);
    });
    // Keep-alive connections with no request in flight would hold close() open.
    const idle = () => { try { http.closeIdleConnections?.(); } catch {} };
    idle();
    setInterval(idle, 250).unref?.();
  };
  process.once("SIGTERM", () => stop("SIGTERM"));
  process.once("SIGINT", () => stop("SIGINT"));
}
`;
}
