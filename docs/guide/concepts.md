# Concepts

## Box
One Linux machine running Tiffin. Locally it is a VM (`tiffin up`). It holds any number
of **projects**.

## Project and resources
A project is described by `tiffin.config.ts`. Tiffin turns it into **resources**:
`app/web`, `service/postgres`, `bucket/uploads`, `env/LOG_LEVEL`, `cron/nightly` and so
on. Each resource has a live state on the machine: *pending*, *ready* or *failed*.

The dashboard's names map to services: Database is `postgres`, Cache is `valkey`, Files
is `storage`, then `auth`, `email` and `analytics`; Jobs are the top-level `queues` and
`crons`. `database`, `cache` and `files` also work in `tiffin.config.ts` and are stored
under the first name (which is what `tiffin pull` writes back).

Each web app is served at `<app>.<domain>` unless it sets `routes`. Addresses are
box-wide, so a second project's `web` app needs its own (`routes: ["blog"]`): the plan
refuses an address another project already serves.

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
  ceiling, not a reservation, and follows the box when you move to a bigger server.
- If both `memoryMB` and `maxSharePercent` are set, the lower wins.

A project at its cap is held there: an app that needs more is stopped for memory and
restarts, and the project's usage says `pressure: "oom"`. Budget changes apply live
through plan and apply; nothing restarts. The box owner can also cap every project that
sets no budget: `tiffin box settings set --body '{"defaultMaxSharePercent":25}'`.

`tiffin projects usage <project>` shows what a project uses against its limits: memory,
headroom (how much more it could take now), CPU, disk, each app's copies and its
services. There is no disk budget yet: Postgres has no per-database quota, so disk is
reported, not enforced (buckets have their own quota, see [Storage](storage.md)).

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
  only", "this key can only change shop"). There is no approval step: the agent's own
  client asks you before destructive tools, and History records everything.
