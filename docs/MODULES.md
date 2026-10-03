# Building a Tiffin module

Tiffin is "your app in a box": one Linux box runs a whole app stack (apps, Postgres,
Valkey, storage, email, auth, queues, observability, analytics), operated by humans
and AI agents through one API. Every box feature is a **module** that plugs into the
platform spine in `internal/platform`. Read `internal/platform/platform.go` first.

Design sources: `../research/2026-10-02-architecture/STACK.md`, `BUILD-PLAN.md`,
and the topic notes there (08 queues, 11 workflows, 14 analytics, 16 protection, 17 design).

## Rules for every agent

- **Stay in your area.** Other agents edit this tree at the same time. Your module
  lives in `internal/mod/<name>/` (plus any packages/apps you were given). Shared files
  you may touch only if your brief says so. Need a change in someone else's area?
  Note it in your final report instead.
- **Commits:** small and path-limited: `git add <explicit paths>` then
  `git commit -m "..." -- <paths>`. Never `git add -A`/`.`, stash, reset, clean or rebase.
  If `.git/index.lock` exists, wait and retry. End messages with
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push.
- **go.mod:** only through the lock: `scripts/golock.sh go get pkg@version`,
  `scripts/golock.sh go mod tidy`. Prefer small, permissive (MIT/BSD/Apache/ISC/MPL
  unmodified) dependencies. Prefer driving a pinned upstream binary over importing a
  giant library when that is cleaner.
- **Heavy jobs** (VM boots, big builds, Playwright, `next build`) go through
  `/Users/bilaltahir/Downloads/personal/projects/throwaway/research/heavy.sh <cmd>`.
- **No external accounts or services.** Downloads of pinned upstream releases (apt,
  GitHub releases, npm) from inside a dev VM are fine. Never touch macOS settings or
  trust stores, never the owner's box `tiffin`, never `~/.tiffin`.
- **Quality bar:** this ships to people who will judge it in minutes. Agent-friendly
  APIs (clear summaries/descriptions, stable error codes, hints), plain-words output,
  honest docs. Test everything you build; leave no stubs that pretend to work.

## The contract

A module registers in `init()` (`platform.Register(&Module{})`; the stub is already
imported by `cmd/tiffin/modules.go`) and implements any of:

| Interface | When it runs | Typical use |
|---|---|---|
| `Provisioner.Provision(ctx, *System)` | `tiffin provision` as root, before the service (re)starts; idempotent | apt packages, pinned downloads (`System.Fetch` needs a sha256), systemd units (`System.Unit`) |
| `APIRegistrar.RegisterAPI(huma.API, *Platform)` | API construction (also with a nil Platform to build the spec) | operations; each becomes a CLI command and an MCP tool automatically |
| `Reconciler.Kinds()/Reconcile(ctx, p, project, address, spec)` | after every apply and once at boot; `spec == nil` means deleted | create DBs, buckets, users; restart apps |
| `EnvProvider.Env(ctx, p, project, app)` | when an app starts | DATABASE_URL, REDIS_URL, S3_*, SMTP_URL ... |
| `RouteProvider.Routes(ctx, p)` | after every converge (`p.RefreshRoutes`) | edge routes (`edge.Route`: host, path prefix, upstreams, file root) |
| `Starter.Start(ctx, p)` | when the box serves | background loops (must return promptly) |
| `Checker.Checks(ctx, p)` | `/v1/status` | health of your service |
| `Orderer.Order()` | sorting | lower first: base 0, data 10, observe 15, storage/email 20, auth/queue 30, runtime 40 |

API operations: use `api.Op(id, method, path, cliWords, risk, summary, description, tags...)`
and `api.Wrap(handler)`; authorize with `api.PrincipalFrom(ctx).Require(tokens.Scope..., project)`;
errors with `api.NewProblem(status, code, detail)` (codes: validation, forbidden, not_found,
conflict, precondition, internal). Risk classes: `api.RiskRead`, `api.RiskWrite`,
`api.RiskDestructive`. Wrap operations whose output contains content others wrote (logs, rows,
emails, file listings) in `api.Untrusted(op)` so MCP fences it as data. Don't embed path-param structs; declare path fields inline.
Resources (things in `tiffin.config.ts`) change only through plan/apply; your reconciler
makes the machine match. Operational actions (deploy, restore, send test email) are API
operations. Small module bookkeeping goes in `p.DB.KVGet/KVPut(ns, key)`.

Secrets: `p.Secrets` (age-encrypted); `p.ProjectEnv(ctx, project, app)` assembles the
full env for an app. Hosts: `p.Host("shop")` → `shop.tiffin.localhost`, `p.URL(host)`.
Data disk: `/var/lib/tiffin` (XFS, reflinks) — use `/var/lib/tiffin/<yourmodule>/`.
The service runs as root (`tiffin serve --box`). Edge access logs: `/var/lib/tiffin/logs/access.log`.

## Your dev box

Each agent gets its own throwaway VM beside the owner's box. Use your assigned names:

```bash
cd /Users/bilaltahir/Downloads/personal/projects/throwaway/tiffin
export TIFFIN_CONFIG_DIR=/tmp/tiffin-dev-<area>  TIFFIN_LIMA_INSTANCE=dev-<area> \
       TIFFIN_LIMA_DISK=<disk, max 7 chars>  TIFFIN_LIMA_PORT=<port>  TIFFIN_LIMA_MEMORY=<e.g. 3GiB>
go build -o /tmp/tiffin-<area> ./cmd/tiffin
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/tiffin-<area>-linux ./cmd/tiffin
/tmp/tiffin-<area> up --binary /tmp/tiffin-<area>-linux     # create or update (provision + self-update)
/tmp/tiffin-<area> status            # every CLI command talks to your dev box
limactl shell dev-<area> -- sudo journalctl -u tiffin -n 200 --no-pager
/tmp/tiffin-<area> down --confirm local   # when you finish: delete it
```

Write unit tests that run on the Mac (`go test ./internal/mod/<name>/...`) and an e2e
test in `e2e/<name>_test.go` (build tag `e2e`) that drives the CLI against a fresh box
the way `e2e/up_test.go` does. Delete your dev box when done.

## Report

Final reply (<450 words): what works end to end (with the exact commands you ran and
real numbers), API operations added, env vars provided, files, tests, gaps, and what the
dashboard needs to show for your module (endpoints + shapes).
