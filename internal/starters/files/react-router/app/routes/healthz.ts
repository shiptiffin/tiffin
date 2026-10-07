// The healthcheck in tiffin.config.ts: a resource route that's cheap and
// doesn't touch the database, so a slow query never takes the app out of rotation.
export function loader() {
  return new Response("ok", { headers: { "Cache-Control": "no-store", "Content-Type": "text/plain; charset=utf-8" } });
}
