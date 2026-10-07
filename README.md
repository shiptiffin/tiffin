# Tiffin

**Your app in a box.**

Tiffin runs your whole app on one Linux machine: your apps, Postgres, Valkey, file
storage, email, sign-in, background jobs and workflows, logs, metrics, errors and
analytics. You and your AI agents operate it through one CLI, one MCP server and a calm
dashboard, all generated from one API.

```bash
tiffin up                                   # a box on your Mac, about a minute
tiffin init && tiffin plan                  # describe the project, see the plan
tiffin apply --confirm <hash>               # apply exactly that plan
tiffin deploy                               # https://<project>.tiffin.localhost:8443
claude mcp add tiffin -- tiffin mcp         # let your agent help, safely
```

> **Status: pre-1.0.** Everything below runs today on a Lima VM on your Mac, and on a real
> server: `tiffin up --provider hetzner` creates one on Hetzner Cloud, and
> `tiffin up --provider ssh --host root@<ip>` installs on any Ubuntu server you can SSH into
> ([quickstart](docs/guide/quickstart.md#run-it-on-a-server)). Cloudflare DNS and encrypted
> off-box backup copies (any S3-compatible bucket) are supported. Interfaces may change.

## What's in the box

| | |
|---|---|
| **Apps** | Next.js (with a Valkey cache handler), TanStack Start, Astro, Hono and other Bun or Node.js apps, FastAPI and other Python apps, any Dockerfile, and static sites. Zero-downtime deploys, rollbacks, preview URLs that sleep when idle, `git push tiffin main`, live logs. |
| **Data** | Postgres 18 per project (pgvector, pg_cron), preview branches cloned in milliseconds, a SQL console, Valkey per project, typed JSONB documents in the SDK. |
| **Files** | S3-compatible buckets (`Bun.s3` works unchanged), presigned links, public files, quotas, a 7-day trash. |
| **Email** | SMTP to any relay; until you set one, every message lands in the dev inbox. Suppressions, bounces, React Email. |
| **Sign-in** | Better Auth for your users: email, magic links, codes, passkeys, Google, GitHub, 2FA, organizations with roles, invites, API keys. Drop-in React components. |
| **Jobs** | Push queues with retries, dead letters, per-key limits and FIFO groups; crons; durable workflows with sleeps, events and human approvals. |
| **Insight** | Metrics, logs, Sentry-compatible error tracking, alerts, and cookieless first-party analytics. |
| **Safety** | Backups and restore drills, rate limits, a proof-of-work bot challenge, an under-attack switch, CrowdSec, a firewall and an opt-in WAF. |

## Built for agents, safe for you

- **Plan, then apply.** Every change shows each step, its risk (reversible, outbound,
  irreversible) and why, and applies only with that plan's hash.
- **API keys.** Each agent gets its own key: all projects or a few, full or read access.
  Claude Code asks you before anything destructive runs; Tiffin records it.
- **Everything is logged and undoable.** Who changed what, which agent session, and why.
- **Untrusted data is fenced.** Logs, rows and emails reach agents marked as data, never
  as instructions.

## Honest limits

It is one machine: if it's down, your app is down. It is made for side projects,
experiments and small apps, not banks. Backups stay on the box unless you copy them off it
(`tiffin backups offsite set`). Read [docs/guide/security.md](docs/guide/security.md) before you put anything
important on it.

## Docs

Start with [the quickstart](docs/guide/quickstart.md), then
[concepts](docs/guide/concepts.md) and [working with agents](docs/guide/agents.md).
Per-service guides: [storage](docs/guide/storage.md), [email](docs/guide/email.md),
[observability](docs/guide/observe.md), [analytics](docs/guide/analytics.md).
Contributing and building a module: [CONTRIBUTING.md](CONTRIBUTING.md).

## Developing

Go 1.27+, [Bun](https://bun.sh) and [Lima](https://lima-vm.io) (`brew install lima`).

```bash
make build      # bin/tiffin
make test       # Go and Bun unit tests
make lint       # gofmt, go vet, staticcheck
make dashboard  # rebuild the embedded dashboard
make sdk        # rebuild the embedded @shiptiffin/sdk (make build runs it; bump the version in packages/sdk when its API changes)
make e2e        # every acceptance test, each on a fresh VM (slow)
make release    # macOS and Linux binaries for arm64 and amd64
```

Apache-2.0. See [NOTICE](NOTICE) for third-party components.
