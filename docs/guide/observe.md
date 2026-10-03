# Logs, metrics, errors and alerts

Every box watches itself and your apps out of the box: no agent to install, no
account to create. Metrics live in [VictoriaMetrics](https://victoriametrics.com)
and logs in VictoriaLogs (both Apache-2.0, pinned releases running on the box,
reachable only through the Tiffin API). Metrics are kept 30 days and logs 14 days
by default.

## What is collected

- **Box metrics** every 15 seconds: CPU, memory, disks, network, every box service
  (up, restarts, memory, CPU) and every app container (memory, CPU).
- **The box's own logs**: Tiffin and every system service, from the journal.
- **App logs**: everything your apps print, with the app, deploy, environment and
  instance attached. JSON lines keep their fields (`msg`, `level` and the rest).
- **Requests at the edge**: every request to an app is a log line (method, path,
  status, duration) and feeds per-app request rate, 5xx errors and p50/p95/p99
  latency. No code needed.
- **Errors your apps report**, with any Sentry SDK (see below).
- **OpenTelemetry**: apps get `OTEL_EXPORTER_OTLP_ENDPOINT` and a key in
  `OTEL_EXPORTER_OTLP_HEADERS`; OTLP metrics and logs land next to everything else,
  labelled with the app that sent them. Traces are not stored yet
  (`OTEL_TRACES_EXPORTER=none` is set for you).

Tiffin tokens, login codes and analytics keys are masked before any line is stored.

## Reading logs and metrics

```bash
tiffin logs query --project shop --query 'level:error' --since 6h
tiffin logs query --project shop --query 'source:edge status:5*'
tiffin logs query --project shop --query '* | stats count() by (app, level)'
tiffin logs query --query 'unit:tiffin.service'        # the box's own logs (box admins)

tiffin observe overview                                # box health now and over the last hour
tiffin observe apps --project shop --since 1h          # requests, errors and latency per app
tiffin metrics query --project shop --query 'sum by (app) (rate(tiffin_http_requests_total[5m]))' --since 1h
```

Queries use [LogsQL](https://docs.victoriametrics.com/victorialogs/logsql/) and
PromQL. Each project's logs are stored separately, so a token for one project can
never read another's, whatever the query; metric queries are pinned to the
token's project. Log lines and error messages are written by apps and visitors:
agents receive them as untrusted data.

## Errors (Sentry-compatible)

Every app gets `SENTRY_DSN` (and `TIFFIN_PUBLIC_SENTRY_DSN` for browser code), so
the official Sentry SDKs report to the box unchanged:

```ts
import * as Sentry from "@sentry/bun";
Sentry.init({ dsn: process.env.SENTRY_DSN });
```

Events are grouped into issues by fingerprint (the exception type and the app's
own stack frames, without line numbers, so a group survives small edits; or the
SDK's explicit fingerprint). A resolved issue that happens again reopens.

```bash
tiffin issues list --project shop --status unresolved
tiffin issues get <iss_id>             # stack, tags, release, URL of the latest events
tiffin issues resolve <iss_id>
tiffin observe ingest --project shop --app web   # the DSNs and OTLP endpoint
```

## Alerts

Rules are checked every 15 seconds. Built in, and editable:

| Rule | Fires when |
|---|---|
| `disk-full` | a disk is more than 85% full |
| `memory-high` | memory is more than 90% used for 5 minutes |
| `cert-expiring` | an HTTPS certificate expires within 72 hours and has not renewed |
| `backup-stale` | the newest backup is more than 26 hours old |
| `error-spike` | a project's apps report more than 20 errors in 5 minutes |
| `service-restarts` | a box service restarted more than 3 times in 15 minutes |
| `service-down` | a box service has not been running for a minute |

Add your own with any PromQL expression:

```bash
tiffin alerts rules put slow-shop --body '{"kind":"promql","expr":"histogram_quantile(0.95, sum by (le) (rate(tiffin_http_request_duration_seconds_bucket{project=\"shop\"}[5m])))","threshold":1.5}'
```

Alerts go to a webhook (JSON, with a `text` field chat tools understand) and by
email through a project's email service, which means the dev inbox until an SMTP
relay is set up:

```bash
tiffin observe settings set --webhook https://hooks.slack.com/... --email-project ops --email you@example.com
tiffin alerts test
tiffin alerts list            # firing now, and recent history with where each notification went
```

Retention: `tiffin observe settings set --metrics-retention 90d --logs-retention 30d`.
