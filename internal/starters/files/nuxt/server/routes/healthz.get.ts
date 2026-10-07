// The healthcheck in tiffin.config.ts: cheap, and it doesn't touch the
// database, so a slow query never takes the app out of rotation.
export default defineEventHandler((event) => {
  setResponseHeaders(event, { "cache-control": "no-store", "content-type": "text/plain; charset=utf-8" });
  return "ok";
});
