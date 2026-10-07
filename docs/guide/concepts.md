# Concepts

## Box
One Linux machine running Tiffin. Locally it is a VM (`tiffin up`). It holds any number
of **projects**.

## Project and resources
A project is described by `tiffin.config.ts`. Tiffin turns it into **resources**:
`app/web`, `service/postgres`, `bucket/uploads`, `env/LOG_LEVEL`, `cron/nightly` and so
on. Each resource has a live state on the machine: *pending*, *ready* or *failed*.

The dashboard's names map to services: Database is `postgres`, KV (key-value,
Redis-compatible, also a cache) is `valkey`, Files is `storage`, then `auth`, `email` and
`analytics`; Jobs are the top-level `queues` and `crons`. `database`, `cache` and `files` also work in `tiffin.config.ts` and are stored
under the first name (which is what `tiffin pull` writes back).

A web app that sets no `routes` is served at a name made from its project (`<domain>` is
the box's domain, or its separate apps domain: see [Domains](domains.md)):

- the project's main app at `<project>.<domain>` (`shop.<domain>`);
- every other web app at `<project>-<app>.<domain>` (`shop-docs.<domain>`);
- workers at none.

The main app is the project's only web app; else the app named like the project; else the
app named `web`; else the first web app in your config. Set `routes` to choose an address
yourself (`routes: ["store"]`, `routes: ["example.com"]`). Addresses are box-wide: the
plan refuses one another project already serves.

An app keeps the address it already has: adding an app later, or upgrading a box whose
apps were served at their app names (`web.<domain>`, the default before addresses were
named after the project), moves nothing. For such an app the plan says so and how to keep
the old name for good (`routes: ["web"]`) or move it (`routes: ["shop"]`).

A project can be duplicated on the box, exported to a file, imported as a new project or
moved to another box ([copying and moving](moving.md)). A **stopped** project (a `stopped`
resource, set by `tiffin projects stop` or a move) keeps its data but runs no apps, and
refuses deploys until it is started again.

## Sharing the box
Launch as many projects as you like: they divide the box between them on their own.
Tiffin keeps memory for itself first (its services, plus Postgres's and Valkey's caches;
about 1.4 GB of a 3 GB box), and the rest is **memory for apps**, shared by every
project. `tiffin box settings get` shows the numbers.

By default a project is **automatic**. It grows into whatever the box has free, so a
busy shop can use most of the box while the others are quiet. It can never take the
platform's memory, and it always leaves 128 MB for each copy the other projects run.
When the box gets tight, each project is protected up to a fair share; one that is over
its share gets swapped out first, so it slows down rather than anyone being killed. CPU
works the same way: full speed when the box is idle, equal shares when projects compete.

When you want a fixed share, give the project a budget in `tiffin.config.ts`:

```ts
export default defineConfig({
  project: "guestbook",
  resources: { memoryMB: 512, cpus: 0.5 },   // or: { maxSharePercent: 25 }
  apps: { web: {} },
});
```

- `memoryMB` caps all of the project's app copies together (production and previews)
  and also reserves that memory for it. All projects' `memoryMB` budgets must fit in the
  memory for apps; the plan says so if they don't.
- `cpus` caps its CPU, in steps of 0.25.
- `maxSharePercent` caps it at a share of the box (memory for apps and CPUs). It is a
  ceiling, not a reservation, and follows the box when you move to a bigger server or
  resize this one (`tiffin up --name <box> --type ...`; the memory for apps is re-read
  within seconds, and Postgres and Valkey are retuned by that `up`).
- If both `memoryMB` and `maxSharePercent` are set, the lower wins.

A project at its cap is held there: an app that needs more is stopped for memory and
restarts, and the project's usage says `pressure: "oom"`. Budget changes apply live
through plan and apply; nothing restarts. The box owner can also cap every project that
sets no budget: `tiffin box settings set --default-max-share-percent 25`.

A limit holds everything the project uses of the box, not only its apps. Its share is
its `maxSharePercent`, the box default, or what its `memoryMB` and `cpus` come to. At 25%:

- its database's queries get a quarter of the CPUs (past that they slow down; other
  projects' queries are not affected) and a quarter of Postgres's 100 connections;
- its KV store is held to the smaller of its `maxMemoryMB` and a quarter of Valkey's memory:
  keys with an expiry are cleared first, then new writes are refused until it is under it
  (reads and deletes still work);
- its builds get a quarter of the CPUs, and past its memory limit (at least 1 GB) they
  slow down instead of failing;
- its apps get a quarter of the weight when the disk is busy.

Every project, limited or not, has database safety limits: a query is stopped after 5
minutes, 30 seconds in a project with a limit (`services.postgres.statementTimeoutSeconds` changes it; one query can raise it
for itself with `SET LOCAL statement_timeout`), a session idle inside a transaction is
closed after 60 seconds, one query's temporary files are capped at a share of the disk,
and a project opens at most 80 connections.

When a limit holds a project back (an app restarted for memory, all its connections in
use, its KV store full) it shows on the project's Usage page and in its History, at most once
an hour each. Queries stopped by the time limit are only counted ("3 queries stopped
today"), on Usage.

`tiffin projects usage <project>` shows what a project uses against its limits: memory,
headroom (how much more it could take now), CPU, disk, its database, KV store and builds
(`sharePercent`, `database`, `cache`, `builds`), each app's copies, its services and its
`limitEvents`.

Disk is shared too: every project's database and files live on the data disk, so the box
guards it. Past 85% full it warns (on Usage and Health), naming the project growing
fastest; past 95% that project becomes read-only (its database refuses writes, its
buckets refuse uploads, its apps' disk folders stop growing) so every other project keeps
running; below 90% it can write again. Each step is a change by the system in the
project's history, and undoing it lets the project write at once. Change the levels with
`tiffin box settings set --disk-warn-percent 85 --disk-stop-percent 95 --disk-resume-percent 90`
(a stop level of 100 only warns). For a tighter share, give a project a storage limit
(off by default; see [Storage](storage.md#storage-limits)).

## Changes
Every change to a project is a **Change**: who made it (a person or an agent, and the
agent's session), why (the intent), exactly what changed, how risky it was, and the
inverse needed to undo it.

The flow is always **plan → review → apply**:

1. `tiffin plan` computes the steps and a **plan hash**. It never changes anything.
2. `tiffin apply --confirm <hash>` applies that exact plan. If anything changed since
   you planned, it refuses and shows the new plan.

The config file is one way in, not the only one. `tiffin projects manifest <project>`
(`GET /v1/projects/{project}/manifest`) returns the project's current manifest, rebuilt
from its resources; the dashboard edits that and sends it through the same plan and
apply. `tiffin pull` writes it back to a readable `tiffin.config.ts` (it never
overwrites a file that differs without `--force`, and shows the diff), plus the source
of any app running a starter, so a project created in the dashboard can move to git at
any time.

## Risk tiers
| Tier | Meaning | Example |
|---|---|---|
| reversible | Undo restores it | add an app, change an env var |
| outbound | Exposes data outside the box | make a bucket public |
| irreversible | Destroys data no inverse can bring back | delete Postgres, delete a bucket |

Every step says, in plain words, why it has its tier.

## Undo
`tiffin undo <change>` applies the change's inverse, after showing you the plan.
It refuses if something the change touched was modified since, so it never silently
overwrites newer work. Deleting a database or bucket keeps a snapshot or trash copy
for seven days.

## People and API keys
- **People** use the dashboard with a role: owner, admin, member or viewer. They sign
  in with a one-time link or a passkey.
- **Agents, scripts and CI** use **API keys**. A key reaches some projects (`all`,
  which includes projects created later, or a list) with **full** access (read, plan
  and apply any change, deleting data included) or **read** access (read and plan
  only). It can expire after 30 or 90 days, or never.
- A key with full access to all projects is the box admin: it also manages keys,
  people and exports. No other key can manage keys.
- Outside its reach a key gets `403 forbidden` with a plain reason ("this key is read
  only", "this key can only reach shop"). There is no approval step: the agent's own
  client asks you before destructive tools, and History records everything.
