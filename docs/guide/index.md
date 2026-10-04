# Tiffin

**Your app in a box.** Tiffin runs your whole app on one Linux machine: the apps,
Postgres, Valkey, file storage, email, sign-in, background jobs, logs and analytics.
You and your AI agents run it through one CLI, one API and one dashboard.

It is made for hobby projects, side projects and experiments: the things you want
online this weekend without stitching six services together.

## What you get

- **One box, everything in it.** No accounts to create for the database, the cache,
  storage, email or analytics. They are all on the box, set up for you.
- **Safe for agents by design.** Every change is planned first, shows how risky each
  step is, and is applied only with that plan's hash. Risky steps need a human's
  passkey. Everything is logged and can be undone.
- **A calm dashboard.** See what changed, who changed it (person or agent) and why.

## What it is not (yet)

Honesty matters more than a big claim:

- **One machine.** If the box is down, your app is down. Backups stay on the box;
  off-site storage is not supported yet. That is fine for side projects; it is not a bank.
- **Pre-1.0.** Interfaces may still change between versions.
- **Local first.** Today a box runs as a VM on your Mac. Servers and Cloudflare are
  not supported yet.

Start with the [quickstart](quickstart.md), then [concepts](concepts.md) and
[working with agents](agents.md).

Services: [apps and deploys](apps.md) · [Postgres, Valkey and backups](data.md) ·
[storage](storage.md) · [email](email.md) · [sign-in](auth.md) ·
[queues and workflows](queues.md) · [observability](observe.md) ·
[analytics](analytics.md) · [domains](domains.md) · [protection](protection.md) · [security model](security.md) ·
[moving a box](moving.md)
