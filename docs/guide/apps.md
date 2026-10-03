# Apps and deploys

An app is one deployable unit in `tiffin.config.ts`:

```ts
apps: {
  web:    { framework: "next", path: "apps/web" },
  api:    { framework: "hono", path: "apps/api", routes: ["web/api"], instances: 2 },
  worker: { framework: "bun",  path: "apps/worker", role: "worker" },
  site:   { framework: "static", path: "site" },
}
```

Bun is the only runtime. `next`, `hono` and any Bun server listening on `$PORT` run as
containers; `static` sites are served straight from the edge.

## Deploy

```bash
tiffin deploy                 # every app in tiffin.config.ts
tiffin deploy --app api       # one app
tiffin deploy --preview pr-12 # a preview at pr-12--web.tiffin.localhost
git push tiffin main          # after `tiffin git-remote --add`
```

The box builds with Railpack and BuildKit, starts the new instances, waits for the
health check, switches traffic with no dropped requests, then drains the old ones. A
failed build or health check leaves the old version serving.

- **Rollback:** `tiffin rollback <app> [deploy]`, to any earlier successful deploy.
- **Logs:** `tiffin logs <app> -f`.
- **Previews** sleep when idle and wake on the first request. Their email always goes
  to the dev inbox.
- **Prebuilt images:** `tiffin deploy --prebuilt image.tar`.

## What your app gets

`PORT`, `NODE_ENV`, `TIFFIN_URL` (its public URL), plus each service's variables:
`DATABASE_URL`, `REDIS_URL`, `S3_*`, `SMTP_URL`, `TIFFIN_AUTH_URL`, `SENTRY_DSN`,
`OTEL_*`, `TIFFIN_QUEUE_*` and your secrets (`tiffin secrets set`). Changing env or
secrets restarts the app with the new values.

## Next.js

Next runs as a long-lived Bun server, so route handlers and server actions run in the
process; long work goes to queues. Add the Valkey cache handler so every instance
shares one cache and `revalidateTag` works across them:

```js
// next.config.mjs
export default { cacheHandler: require.resolve("tiffin-sdk/next/cache-handler"), cacheMaxMemorySize: 0 };
```

Templates: `templates/hello-hono`, `hello-next`, `static-site`, `queues-worker`.
