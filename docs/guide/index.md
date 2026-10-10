# Tiffin

**Your app in a box.** Tiffin runs your whole app on one Linux machine: the apps,
Postgres, Valkey, file storage, email, sign-in, background jobs, logs and analytics.
You and your AI agents run it through one CLI, one API and one dashboard.

It is made for hobby projects, side projects and experiments: the things you want
online this weekend without stitching six services together.

A box is one Linux server you own. ShipTiffin can set it up in your Hetzner account for you
(managed), or you run it yourself, free, on Hetzner or any Ubuntu server. The
[quickstart](quickstart.md) shows all three.

## What you get

- **One box, everything in it.** No accounts to create for the database, the KV store,
  storage, email or analytics. They are all on the box, set up for you.
- **Safe for agents by design.** Every change is planned first, shows how risky each
  step is, and is applied only with that plan's hash. Your agent's client asks you
  before destructive steps. Everything is logged, and most changes can be undone.
- **A calm dashboard.** See what changed, who changed it (person or agent) and why.

## What it is not (yet)

Honesty matters more than a big claim:

- **One machine.** If the box is down, your app is down. Backups stay on the box unless
  you [copy them off it](data.md#copies-off-the-box) to a bucket. That is fine for side projects; it is not a bank.
- **Pre-1.0.** Interfaces may still change between versions.

[What works and what doesn't](limits.md) lists every framework, limit and gap in one place.

Start with the [quickstart](quickstart.md), or let your coding agent do it:
[set up ShipTiffin with your agent](agent-onboarding.md). Then read [concepts](concepts.md)
and [working with agents](agents.md). To run an agent of your own on the box, see
[always-on agents](always-on-agents.md).

Services: [apps and deploys](apps.md) · [Postgres, Valkey and backups](data.md) ·
[storage](storage.md) · [email](email.md) · [sign-in](auth.md) ·
[queues and workflows](queues.md) · [observability](observe.md) ·
[analytics](analytics.md) · [domains](domains.md) · [protection](protection.md) · [security model](security.md) ·
[copying and moving](moving.md) · [managed boxes (ShipTiffin)](managed.md) · [what works and what doesn't](limits.md)
