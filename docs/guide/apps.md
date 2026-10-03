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

## Without a checkout: templates and git URLs

The dashboard creates apps without any files on your machine; so can the CLI and agents.

```bash
tiffin templates list                                  # starters shipped inside tiffin
tiffin deploys template shop api --template hono-postgres
tiffin deploys git shop web --url https://github.com/owner/repo --ref main --path apps/web
```

Four starters ship in the binary: `static-site`, `hono-postgres` (a notes API that
creates its table on boot), `guestbook` (page + API + Postgres + Valkey + analytics in
one Hono app) and `next-postgres` (App Router, reads and writes Postgres). Each lists
the manifest fragment it needs: add that to the project (see
[Concepts](concepts.md#changes)), apply, then deploy the template.

A git URL deploy shallow-clones one commit of a **public https** repository on the box
(no credentials, public hosts only, no submodules, 512 MB and 3 minutes at most) and
builds it like `tiffin deploy`; the clone shows in the build log. For private code, push
to the box instead.

## What your app gets

`PORT`, `NODE_ENV`, `TIFFIN_URL` (its public URL), plus each service's variables:
`DATABASE_URL`, `REDIS_URL`, `S3_*`, `SMTP_URL`, `TIFFIN_AUTH_URL`, `SENTRY_DSN`,
`OTEL_*`, `TIFFIN_QUEUE_*` and your secrets (`tiffin secrets set`). Changing env or
secrets restarts the app with the new values.

## Next.js

Next runs as a long-lived Bun server (`bun --bun next start`), so route handlers and
server actions run in the process; long work goes to queues. With two or more
instances, add Valkey and the `tiffin-sdk/next` cache handlers so every instance shares
one cache and `revalidateTag` reaches all of them. Setup is in
`packages/sdk/src/next/README.md`; `templates/hello-next` has it wired up.

Templates: `templates/hello-hono`, `hello-next`, `static-site`, `queues-worker`.
