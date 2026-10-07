import { createFileRoute } from "@tanstack/react-router";

// The healthcheck in tiffin.config.ts: cheap, and it doesn't touch the
// database, so a slow query never takes the app out of rotation.
export const Route = createFileRoute("/healthz")({
  server: {
    handlers: {
      GET: () => new Response("ok", { headers: { "Cache-Control": "no-store" } }),
    },
  },
});
