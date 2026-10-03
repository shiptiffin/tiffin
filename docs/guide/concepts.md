# Concepts

## Box
One Linux machine running Tiffin. Locally it is a VM (`tiffin up`). It holds any number
of **projects**.

## Project and resources
A project is described by `tiffin.config.ts`. Tiffin turns it into **resources**:
`app/web`, `service/postgres`, `bucket/uploads`, `env/LOG_LEVEL`, `cron/nightly` and so
on. Each resource has a live state on the machine: *pending*, *ready* or *failed*.

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
overwrites a file that differs without `--force`, and shows the diff), so a project
created in the dashboard can move to git at any time.

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

## Tokens, people and approvals
- **People** use the dashboard with a role: owner, admin, member or viewer.
- **Agents** use tokens. A token has scopes (read, plan, apply:reversible,
  apply:outbound, apply:irreversible, tokens) and projects. It can never hand out more
  than it has, and agent tokens always expire.
- When an agent's plan goes beyond its token, it gets an **approval request**. A human
  approves it in the dashboard with their **passkey**. The approval is for that exact
  plan, that agent, once.
