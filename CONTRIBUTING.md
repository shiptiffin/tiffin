# Contributing to Tiffin

Tiffin is "your app in a box": one Linux box runs a whole app stack (apps, Postgres,
Valkey, storage, email, auth, queues, observability, analytics), operated by humans
and AI agents through one API. Every box feature is a **module** that plugs into the
platform spine in `internal/platform`. Read `internal/platform/platform.go` first.

Tooling and the `make` targets are in the README's "Developing" section.

## Ground rules

- **Small, focused commits** that touch only the paths they need.
- **Dependencies:** change `go.mod` through `scripts/golock.sh go get pkg@version` and
  `scripts/golock.sh go mod tidy` (it serialises concurrent edits). Prefer small,
  permissive (MIT/BSD/Apache/ISC, MPL unmodified) dependencies, and prefer driving a
  pinned upstream binary over importing a giant library when that is cleaner.
- **Dev boxes only.** Develop against a separate dev box (below), never your default
  box or `~/.tiffin`. Don't change host settings or trust stores from tests.
- **Quality bar:** agent-friendly APIs (clear summaries and descriptions, stable error
  codes, hints), plain-words output, honest docs. Test what you build; no stubs that
  pretend to work.

## The contract

A module lives in `internal/mod/<name>/`, registers in `init()`
(`platform.Register(&Module{})`, with a blank import in `cmd/tiffin/modules.go`) and
implements any of:

| Interface | When it runs | Typical use |
|---|---|---|
| `Provisioner.Provision(ctx, *System)` | `tiffin provision` as root, before the service (re)starts; idempotent | apt packages, pinned downloads (`System.Fetch` needs a sha256), systemd units (`System.Unit`) |
| `APIRegistrar.RegisterAPI(huma.API, *Platform)` | API construction (also with a nil Platform to build the spec) | operations; each becomes a CLI command and an MCP tool automatically |
| `Reconciler.Kinds()/Reconcile(ctx, p, project, address, spec)` | after every apply and once at boot; `spec == nil` means deleted | create DBs, buckets, users; restart apps |
| `Committer.Committed(ctx, p, change)` | as a change commits, before apply returns and before the reconciler's pass | put in force what callers count on once apply returns (a bigger disk folder); quick, idempotent, reads the committed state |
| `EnvProvider.Env(ctx, p, project, app)` | when an app starts | DATABASE_URL, REDIS_URL, S3_*, SMTP_URL ... |
| `RouteProvider.Routes(ctx, p)` | after every converge (`p.RefreshRoutes`) | edge routes (`edge.Route`: host, path prefix, upstreams, file root) |
| `Starter.Start(ctx, p)` | when the box serves | background loops (must return promptly) |
| `Checker.Checks(ctx, p)` | `/v1/status` | health of your service |
| `LossEstimator.EstimateLoss(ctx, p, project, op)` | every plan with an irreversible op; the plan waits at most 200 ms in all | say what the op destroys (`change.Loss`: "18,204 rows in 12 tables · 41 MB"); return nil, nil for ops that aren't yours; read-only and fast |
| `PlanChecker.CheckPlan(ctx, p, project, desired)` | every plan/apply of a manifest | refuse a desired state the box cannot run (an `api.Problem` 422 with a hint); fast and read-only |
| `UsageReporter.ProjectUsage(ctx, p, project)` | in the background for `GET /v1/projects/{project}/usage` (cached 30 s) | bytes the project holds in your service (`Disk`: database, files or kv) and a few counts; nil, nil when it doesn't use it |
| `Orderer.Order()` | sorting | lower first: base 0, data 10, observe 15, storage/email 20, auth/queue 30, budget 35, runtime 40 |

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
full env for an app. Hosts: `p.Host("shop")` → `shop.<box domain>`, `p.URL(host)`, `p.DashboardHost()`; the domain
can change (`tiffin domain set` restarts the service), so never store full host names. `p.Reach` says how the
world reaches the box (public IPs, ACME or the internal CA); `p.DNS` (nil without a connected provider) sets
DNS records, e.g. email's SPF/DKIM/DMARC.
Data disk: `/var/lib/tiffin` (XFS, reflinks) — use `/var/lib/tiffin/<yourmodule>/`.
The service runs as root (`tiffin serve --box`, unit `tiffin`). The HTTPS edge (Caddy and the switchboard that
routes to app instances) is its own process (`tiffin edge`, unit `tiffin-edge`, ports held by `tiffin-edge.socket`),
so restarting or updating `tiffin` never interrupts the apps; `tiffin` sends it routes and instances over
`/var/lib/tiffin/platform/edge.sock` (internal/edge/proto.go). Edge access logs: `/var/lib/tiffin/logs/access.log`.

## A dev box

A dev box is a second Lima VM beside your default one, with its own instance, disk,
port and config dir, chosen with environment variables:

```bash
export TIFFIN_CONFIG_DIR=/tmp/tiffin-dev  TIFFIN_LIMA_INSTANCE=tiffin-dev \
       TIFFIN_LIMA_DISK=tdev  TIFFIN_LIMA_PORT=18443  TIFFIN_LIMA_MEMORY=3GiB
# TIFFIN_LIMA_DISK is at most 7 characters; TIFFIN_LIMA_CPUS is optional.
go build -o /tmp/tiffin-dev ./cmd/tiffin
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/tiffin-dev-linux ./cmd/tiffin
/tmp/tiffin-dev up --binary /tmp/tiffin-dev-linux    # create or update (provision + self-update)
/tmp/tiffin-dev status                               # every CLI command talks to the dev box
limactl shell tiffin-dev -- sudo journalctl -u tiffin -u tiffin-edge -n 200 --no-pager
/tmp/tiffin-dev down --confirm local                 # delete it when you're done
```

## Tests

Unit tests run on the host: `go test ./internal/mod/<name>/...` (`make test` runs all Go
and Bun tests; `make lint` runs gofmt, go vet and staticcheck). A module also gets an e2e
test in `e2e/<name>_test.go` (build tag `e2e`) that drives the CLI against a fresh box
the way `e2e/up_test.go` does; `make e2e` runs them all, each on a fresh VM (slow).

## Releases

Pushing a `v*` tag runs `.github/workflows/release.yml`: `make release` builds
`dist/tiffin-<os>-<arch>` reproducibly (trimmed paths, no build ID, the commit's date),
`make release-sign` writes a minisign-signed manifest per channel (`dist/<channel>/manifest.json`),
and the workflow publishes the builds and points the rolling `channel-stable` /
`channel-edge` releases at the new manifest, which boxes read. Per-release settings
(`Rollout: 10`, `Min-Version: 1.3.0`, `Edge-Restart: true`) go in the annotated tag's
message; running the workflow by hand with a tag and a percentage widens a rollout.

The signing key never goes in the repository. Make one with
`go run ./cmd/tiffin-release keygen -out ~/tiffin-release.key` (or `minisign -G -W`), put its
public key in `internal/release/keys.go` and the secret key file's contents in the
`TIFFIN_RELEASE_KEY` repository secret. To rotate, add the new public key, release, then
sign with the new key; drop the old one once every box runs a build that trusts the new.
`go run ./cmd/tiffin-release verify dist/stable/manifest.json` (or `minisign -Vm ... -P <key>`)
checks a signed manifest.
