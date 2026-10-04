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
tiffin deploys git shop web --git-url https://github.com/owner/repo --ref main --path apps/web
```

Four starters ship in the binary: `static-site`, `hono-postgres` (a notes API that
creates its table on boot), `guestbook` (page + API + Postgres + Valkey + analytics in
one Hono app) and `next-postgres` (App Router, reads and writes Postgres). Each lists
the manifest fragment it needs: add that to the project (see
[Concepts](concepts.md#changes)), apply, then deploy the template.

A git URL deploy shallow-clones one commit of a **public https** repository on the box
(no credentials, public hosts only, no submodules, 512 MB and 3 minutes at most) and
builds it like `tiffin deploy`; the clone shows in the build log. For private code, push
to the box instead, or connect GitHub.

## Deploy from GitHub

Connect the box to GitHub once, and it deploys like Vercel: every push to an app's
production branch goes live, every pull request gets a preview at its own address (with
one comment on the pull request, kept up to date), and closing the pull request removes
the preview.

**1. Connect.** Dashboard › Settings › Git › **Connect GitHub**. The box makes its own
GitHub App (the *manifest flow*: no shared secrets, nothing to copy): GitHub asks you to
confirm `tiffin-<your box>` for your account (tick *In an organization* for an org), then
you choose which repositories it may see. The app's private key and webhook secret stay
on the box, encrypted with the box's own key. The box needs a public HTTPS address first,
because GitHub delivers pushes to `https://<dashboard>/v1/github/webhook`.

The app asks for: code (read), metadata (read), pull requests (write, for the preview
comment), commit statuses (write) and deployments (write). Events: push and pull request.

**2. Import a repository.** New project › **Import from GitHub**: search your repositories
(private ones included), pick one, then its production branch and folder (monorepos list
each app they find, with the framework the box would use), add environment variables
(they're saved as encrypted secrets) and create. That writes the app with a `git` block
and deploys the branch's latest commit:

```ts
apps: {
  web: { framework: "next", git: { repo: "acme/shop", branch: "main", path: "apps/web" } },
}
```

| `git` field | Default | |
|---|---|---|
| `repo` | (required) | `owner/name` on GitHub |
| `branch` | `"main"` | the production branch: every push to it deploys |
| `path` | the top | the app's folder in the repository |
| `previews` | `"same-repo"` | `"same-repo"`: a preview per pull request from a branch of this repository; `"forks"`: forks too; `"off"` |

`tiffin pull` writes the block back, so the config round-trips.

**3. Push.** That's it. What happens on GitHub:

- the commit gets a status `tiffin/<project>/<app>`: *pending* while it builds, then
  *success* (linking the live address) or *failure* (linking the build log);
- a GitHub deployment per deploy (`tiffin/<project>/<app>`, previews as transient
  environments `…/pr-12`);
- a pull request's preview lives at `pr-12--web.<domain>`; one comment says
  "Preview of `web`: https://pr-12--web.example.com · built in 34 s · logs".

Rapid pushes to one branch coalesce: while one builds, only the newest waiting push is
built next (the ones in between show as *skipped*). A commit message with `[skip deploy]`
or `[skip ci]` deploys nothing. On the app's page: the connected repository and branch,
each version's commit (message, author, SHA), **Redeploy** and **Disconnect repo**.

**Security.** Deliveries must carry a valid `X-Hub-Signature-256` (HMAC-SHA256 with the
app's webhook secret, compared in constant time); a delivery id is accepted once, and
events older than an hour are refused. Each clone uses a fresh token that can only read
that one repository, handed to git through its environment (never a file or the command
line) and revoked as soon as the clone is done. **Pull requests from forks are not built**
unless the app says `previews: "forks"`: a fork's code would run on your box with the
project's env and secrets.

**From the terminal or an agent** (every step is an API operation, so also an MCP tool):

```bash
tiffin github status                       # connected? installed where? recent deliveries
tiffin github repos --q shop               # repositories the app can see
tiffin github repo acme shop               # branches, latest commit, folders + framework
# add apps.web.git to the manifest, then plan and apply it (projects manifest → plan → apply)
tiffin deploys github shop web             # deploy the branch now (Redeploy); --ref for another
```

Connecting needs a person in a browser (`tiffin github connect` returns the form GitHub
expects); agents should ask the owner to click Connect GitHub. `tiffin github disconnect`
forgets the app (delete it on GitHub too: Settings › Developer settings › GitHub Apps).

**A shared app instead** (a hosted box, or an app you already have): `tiffin github use-app
--app-id 123 --private-key "$(cat app.pem)" --webhook-secret … [--client-id … --client-secret
… --public]`, or the operator writes `/var/lib/tiffin/platform/github.json`:

```json
{ "app": { "id": 123, "privateKeyFile": "/etc/tiffin/github-app.pem", "webhookSecretFile": "/etc/tiffin/github-webhook",
           "clientId": "Iv1.…", "clientSecretFile": "/etc/tiffin/github-client", "public": true } }
```

The app's webhook must point at `<box>/v1/github/webhook`. A **public** app is installed by
other accounts too, so the box only acts for installations made from it: enable *Request
user authorization (OAuth) during installation* on the app with `<box>/v1/github/setup` as
a callback URL; after an install the box checks, with GitHub sign-in, that the person can
reach that installation. The same file takes `"apiUrl"` and `"webUrl"` for GitHub
Enterprise Server.

**Try it for real (once the box has its public domain):**

1. Dashboard › Settings › Git › Connect GitHub → confirm on GitHub → choose a repository
   (a small Next.js or static one) → you land back on Settings › Git with "Connected".
2. New project › Import from GitHub → pick it → Create. The build log streams; the commit
   on GitHub shows a pending, then green, `tiffin/…` check.
3. Push a commit to the branch: a new version appears on the app's page within seconds of
   the push, with its message and author.
4. Open a pull request: a preview comment appears, then updates with the address; open it.
   Push to the pull request: the same comment updates. Close it: the preview is removed
   and the comment says so.
5. Settings › Git › Recently lists each delivery; GitHub's app settings › Advanced lists
   them too (all should be 2xx).

## Sharing the box

Every app copy of a project, previews included, runs inside the project's share of the
box. Apps don't need a memory setting: by default their copies share the project's
memory, and the project grows into whatever the box has free (see
[Sharing the box](concepts.md#sharing-the-box)). To give a project a fixed share, set
`resources` at the top of `tiffin.config.ts`; to also stop one copy from crowding out
its siblings, give the app its own cap:

```ts
resources: { memoryMB: 1024 },               // the whole project
apps: { web: { instances: 2, memoryMB: 384 } } // each web copy, within that
```

`tiffin projects usage shop` shows how much the project uses, how much more it could
take, and whether it ran out lately. When an app is stopped for memory, it restarts on
its own and the usage says `pressure: "oom"`: raise the budget or find the leak.

## What your app gets

`PORT`, `NODE_ENV`, `TIFFIN_URL` (its public URL), plus each service's variables:
`DATABASE_URL`, `REDIS_URL`, `S3_*`, `SMTP_URL`, `TIFFIN_AUTH_URL`, `SENTRY_DSN`,
`OTEL_*`, `TIFFIN_QUEUE_*` and your secrets (`tiffin secrets set`). Changing env or
secrets restarts the app with the new values. Setting, copying or deleting a secret is a
change in History (`-m` gives the reason) that `tiffin undo <id>` reverts, putting back the
old value: the change log keeps values only encrypted to the box key. Destroying a project
deletes its secrets too (its plan lists them).

## Next.js

Next runs as a long-lived Bun server (`bun --bun next start`), so route handlers and
server actions run in the process; long work goes to queues. With two or more
instances, add Valkey and the `tiffin-sdk/next` cache handlers so every instance shares
one cache and `revalidateTag` reaches all of them. Setup is in
`packages/sdk/src/next/README.md`; `templates/hello-next` has it wired up.

Templates: `templates/hello-hono`, `hello-next`, `static-site`, `queues-worker`.
