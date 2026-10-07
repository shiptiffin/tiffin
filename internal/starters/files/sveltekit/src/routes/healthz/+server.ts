// The healthcheck in tiffin.config.ts: cheap, and it doesn't touch the
// database, so a slow query never takes the app out of rotation.
export const GET = () => new Response("ok", { headers: { "cache-control": "no-store", "content-type": "text/plain; charset=utf-8" } });
