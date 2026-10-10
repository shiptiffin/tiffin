# Quickstart

Three steps: get a box, connect your computer and your agent to it, then ship a project.
A box is one Linux server you own, with Tiffin and every service on it.

## 1. Get a box

Pick one way. All three give you the same box.

### Managed (recommended)

Go to [shiptiffin.com/start](https://shiptiffin.com/start). Sign in, pay, and paste a
Hetzner Cloud API token (make a new project for ShipTiffin in the Hetzner Cloud console,
then Security → API tokens → Generate API token, **Read & Write**). Pick a name, a size and
a place. ShipTiffin builds the box in **your own** Hetzner account in about five minutes,
at `<name>.shiptiffin.app`. You install nothing for this.

It costs $19 a month per box ($12 for the first 100 customers, locked for 24 months), plus
the server, which Hetzner bills you for: about $10 a month before VAT for the smallest.
You get updates, monitoring, encrypted off-server backups and support.
[How managed boxes work](https://shiptiffin.com/docs/managed.md).

When it's ready, click **Open your dashboard**. It signs you in once. Add a passkey in the
dashboard's Settings: after that you sign in on the box itself.

### Self-host on Hetzner (free)

Install the `tiffin` CLI (macOS or Linux; on Windows, inside
[WSL](https://learn.microsoft.com/windows/wsl/install)). Make a **Read & Write** API token
in the Hetzner Cloud console (your project → Security → API tokens). Then:

```bash
curl -fsSL https://shiptiffin.com/install.sh | sh
export HCLOUD_TOKEN=...                               # the Hetzner token
tiffin up --provider hetzner --name shop --dry-run    # what it makes, and the monthly price
tiffin up --provider hetzner --name shop              # a few minutes
```

It makes the server, a 40 GB data volume, a firewall and an SSH key in your Hetzner
project. At the end it prints the dashboard address and a one-time sign-in link. Open the
link and add a passkey in Settings. On that computer, `tiffin login` prints a new link any
time (`--open` opens it on a Mac).
[More on Hetzner boxes](#hetzner).

### Self-host on any Ubuntu server (free)

Any Ubuntu 26.04 server (24.04 also works) you can reach over SSH, as root or a user with
passwordless sudo:

```bash
curl -fsSL https://shiptiffin.com/install.sh | sh
tiffin up --provider ssh --name shop --host root@203.0.113.5
```

It prints the dashboard address and a one-time sign-in link, as above.
[More on Ubuntu servers](#any-ubuntu-server).

Until a box has a domain of its own, a self-hosted box answers at
`https://dashboard.<server IPv4, dots as dashes>.sslip.io`, with a real certificate
([domains](https://shiptiffin.com/docs/domains.md)).

## 2. Connect your computer and your agent

### A box you made with `tiffin up`

Nothing to do. The computer that ran `tiffin up` remembers the box (in `~/.tiffin`) and
every `tiffin` command talks to it. Check with `tiffin whoami`. Connect Claude Code:

```bash
claude mcp add -s user tiffin -- tiffin mcp
```

`tiffin mcp` uses the box's own agent key, so there is no key to paste. `-s user` makes
it work in every folder; without it, Claude Code adds the server for the current folder
only.

### A managed box, or a box from another computer

Make an API key in the dashboard: **Settings › API keys → Create key**. For your own use,
pick All projects and Full access (or one project, to keep it narrower). The key is shown
once. There is no saved login for a box you didn't make with `tiffin up`: the CLI reads
the box's address and the key from two environment variables. Then install the CLI
and point it at the box:

```bash
curl -fsSL https://shiptiffin.com/install.sh | sh
export TIFFIN_URL=https://dashboard.<name>.shiptiffin.app   # the dashboard's address
export TIFFIN_TOKEN=<key>
tiffin whoami                                               # shows the key's name
```

Put the two `export` lines in your shell profile (`~/.zshrc` or `~/.bashrc`) to keep them.

For your coding agent, make a second key named after it (for example `claude-code`). The
dialog that shows a new key also shows the Claude Code line for your box:

```bash
claude mcp add -s user --transport http tiffin https://dashboard.<name>.shiptiffin.app/mcp \
  --header "Authorization: Bearer <key>"
```

Codex, Cursor and VS Code: see [connecting an agent](https://shiptiffin.com/docs/agent-onboarding.md#connecting-an-agent).

Each agent should have its own key, so History shows who did what. A key with full access
to all projects can do what you can. Claude Code asks you before it runs anything
destructive (deleting a database, say) unless you've allowed that tool, and most changes
can be undone. For an agent that should only touch one project, or only read, make a
narrower key: `tiffin tokens create --name ci --projects shop --access read`.

## 3. Ship your first project

The quickest way is the dashboard: **New project** starts one from a starter app, or
imports a repository from GitHub (it asks you to connect GitHub the first time). After an
import, every push deploys, and every pull request gets a preview.

Or from your app's folder on your computer. No app yet? Make a Next.js one first:

```bash
npx create-next-app@latest hello --yes
cd hello
tiffin init
```

`tiffin init` writes `tiffin.config.ts`, plus `AGENTS.md` and an agent skill so your
coding agent knows how to work with the box. The project is named after the folder:

```ts
import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "hello",
  apps: {
    web: { framework: "next" },
  },
  services: {
    postgres: {},
  },
});
```

Database, KV, Files, Email and Analytics are always there; list a service only to set its
options, and add `auth: {}` for sign-in. Apps use `@shiptiffin/sdk`. In a folder with a
`package.json`, `tiffin init` adds the copy that ships inside `tiffin` as
`vendor/shiptiffin-sdk-<version>.tgz` (`tiffin sdk add` does it later), so no registry is
needed: commit `vendor/` and run `npm install` or `bun install`. An app that installs it
from npm (`bun add @shiptiffin/sdk`) keeps that.

Then plan, apply and deploy:

```bash
tiffin plan
tiffin apply --confirm <hash> -m "Set up hello"
tiffin deploy
```

The plan lists every step, its risk and why. Nothing changes until you confirm with that
exact plan's hash. `tiffin deploy` builds on the box. Your app is then live at
`https://hello.<box domain>`: an app that sets no `routes` is served at its project's name
([concepts](https://shiptiffin.com/docs/concepts.md)). `tiffin logs web` shows its logs.

Next: [apps and deploys](https://shiptiffin.com/docs/apps.md) for previews, env vars and GitHub, and
[domains](https://shiptiffin.com/docs/domains.md) to use your own domain. Or let your agent carry on:
[working with agents](https://shiptiffin.com/docs/agents.md).

## Running your own server

This part is for boxes you made with `tiffin up`. You resize or delete a managed box from
your shiptiffin.com account instead.

### Hetzner

`tiffin up --provider hetzner --name shop --dry-run` creates nothing: it prints the
server, a 40 GB data volume, a firewall and an SSH key, and the monthly price from
Hetzner's own price list. Drop `--dry-run` to create them. Instead of `HCLOUD_TOKEN`,
`--token-file <path>` reads the token from a file. The defaults are
a `cax11` (ARM, 2 vCPU, 4 GB) in `fsn1` on Ubuntu 26.04; change them with `--type` and
`--location`. To use your own SSH key instead of one Tiffin
makes, pass `--ssh-key ~/.ssh/id_ed25519` (or set `HCLOUD_SSH_KEY`); only the public
half is uploaded, and a copy already in the project is reused.

Run `tiffin up --name shop` again to update it: Tiffin and its HTTPS edge restart on the
new build (the edge's ports are held meanwhile, so no connection is refused). To make it
bigger in place:

```bash
tiffin up --name shop --type cax21 --dry-run   # old and new size, and the monthly price
tiffin up --name shop --type cax21             # shows the same plan and asks; --yes skips the question
tiffin up --name shop --volume-size 80         # grow the data volume
```

A new type restarts the box for about 2 minutes (Tiffin stops, the server shuts down,
Hetzner changes it, it starts again and Postgres, Valkey and the app memory pool are
retuned); `up` reports the downtime it measured. The server's own disk stays as it is,
so a smaller type stays possible later. ARM (`cax`) and x86 (`cx`, `cpx`, `ccx`) types
cannot be swapped: Tiffin says which types this box can take. Growing the volume has no
downtime, and volumes never shrink. Delete protection does not get in the way. Settings ›
This box in the dashboard lists the next sizes with prices and the command to run.

To delete it:

```bash
tiffin down --confirm shop                 # server, firewall, key; the data volume is kept
tiffin down --confirm shop --delete-data   # the volume too
```

`tiffin down` without `--confirm` shows what would go, and what keeps costing money.

The firewall lets SSH in only from where you run `tiffin`: each `tiffin up` adds your
current address (keeping your last five) before it connects, and says so. Locked out
anyway? Run `tiffin up --name shop --ssh-from any` (SSH stays keys-only and CrowdSec
still bans brute force), or open the firewall in the Hetzner console under Firewalls,
or boot the server's rescue system there.

Already made a server in the Hetzner console? Adopt it instead (by name or ID, with the
key that logs in to it as root):

```bash
tiffin up --provider hetzner --adopt my-server --ssh-key ~/.ssh/id_ed25519 --dry-run
```

Adopting labels the server, its volume and its IPs, keeps the IPv4 address if the server
is ever deleted, turns on delete protection (`--no-protect` skips it; while it is on, `tiffin down`
needs `--unprotect`), adds the firewall and installs Tiffin. An empty volume moves from
Hetzner's `/mnt/HC_Volume_<id>` to Tiffin's data directory; a volume with files on it is
never moved or formatted.

### Any Ubuntu server

To keep your data on a disk of its own:

```bash
tiffin up --provider ssh --name shop --host root@203.0.113.5 --data-disk /dev/sdb
```

`--data-disk` is optional: a blank disk is formatted XFS for your data (one with a
filesystem is used as it is). Without one, data lives on the root disk; that works, but
database branches copy files instead of sharing them unless the disk is XFS. A data disk
added later never hides data already on the root disk: `up` refuses and says how to move it.

Tiffin checks the server's SSH host key against your own `~/.ssh/known_hosts` too, so
connect once with `ssh` first and check the fingerprint: Tiffin then uses the key you
accepted. A server you never reached is trusted on first use, and a different key is
refused after that. `tiffin down --confirm shop` stops Tiffin, its sites and its apps and
forgets the box on your computer; the server and its data stay.

To make it bigger, resize it at your host (more memory or CPUs, a bigger data disk), then
run `tiffin up --name shop`: it retunes Postgres, Valkey and the memory apps share to the
new machine, and grows an XFS data disk to fill a disk that grew (a partition you grow yourself).

### What Tiffin does to the server

It turns on daily security updates, allows SSH keys only, lets in only SSH, HTTP and
HTTPS, bans brute-force IPs with CrowdSec (never the IP you run `tiffin` from), keeps
the clock in sync, adds a swap file and caps log size. A kernel update never reboots the
server unless you choose a time: `tiffin up --name shop --reboot-window 04:00`.
`tiffin status` shows all of it, including a reboot that is waiting.

## Tiffin's own updates

This applies to every box, managed ones too.

A box running a Tiffin release keeps itself on the newest release of its channel
(`stable`, or `edge` for pre-releases too). Once a day it reads the channel's release
manifest and checks its signature against the release keys built into Tiffin; a manifest
or a build that does not match is refused, and so is anything older than what runs. In
the maintenance window (`--reboot-window`, half an hour in, after the Postgres update) it
installs a new release by itself: it downloads the build and checks its sha256, takes a
backup and waits for it, then switches to the new build the way `tiffin up` does. Apps
keep serving throughout; if the new build is not healthy within 90 seconds, the previous
one comes back. A release that changed the edge restarts it too (the ports are held
meanwhile, about a tenth of a second). A release may go to a share of boxes first: each
box knows whether it is in that share.

```bash
tiffin update status                      # version, channel, what is out, recent updates
tiffin update check                       # read the manifest now
tiffin update apply                       # install the newest release now
tiffin update settings --auto=false       # only when you run update apply
tiffin update settings --window 03:30     # a window for updates of its own
tiffin update settings --channel edge
```

Each update is in the audit log (`box.update`), and one that failed or rolled back sends
an alert. Settings › Updates in the dashboard shows the same, with the switch. A
development build (from source) updates with `tiffin up` only.
