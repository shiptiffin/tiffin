# Quickstart

You need a Mac with [Lima](https://lima-vm.io) (`brew install lima`) and the `tiffin` binary.

## 1. Make a box

```bash
tiffin up
```

This creates an Ubuntu 26.04 VM on your Mac, installs Tiffin and its services, and checks it
answers over HTTPS. It takes about a minute the first time (it downloads the Ubuntu
image) and seconds after that. Run it again any time: it updates Tiffin in place and
rolls back by itself if an update is unhealthy.

Trust the box's certificate once so your browser doesn't warn (macOS asks for your password):

```bash
tiffin trust
```

## 2. Describe your project

```bash
mkdir hello && cd hello
tiffin init
```

`tiffin init` writes `tiffin.config.ts`, plus `AGENTS.md` and an agent skill so your
coding agent knows how to work with the box. Apps use `@shiptiffin/sdk`: install it from npm
(`bun add @shiptiffin/sdk`), or use the copy that ships inside `tiffin`: `tiffin init` (or
`tiffin sdk add` once the app has a `package.json`) vendors it as
`vendor/shiptiffin-sdk-<version>.tgz` with `"@shiptiffin/sdk": "file:./vendor/…"`, so no
registry is needed. Commit `vendor/` and run `bun install`; builds on the box install it from
there. An app that installs it from npm keeps that. Edit the config:

```ts
import { defineConfig } from "@shiptiffin/sdk";

export default defineConfig({
  project: "hello",
  apps: { web: { framework: "next" } },
  services: { postgres: {}, valkey: {}, auth: {}, email: {} },
});
```

## 3. Plan, then apply

```bash
tiffin plan
tiffin apply --confirm <hash> -m "Set up hello"
```

The plan lists every step, its risk and why. Nothing changes until you confirm with
that exact plan's hash.

## 4. Deploy

```bash
tiffin deploy
```

Your app is live at `https://hello.tiffin.localhost:8443`: an app that sets no `routes` is
served at its project's name ([concepts](concepts.md)).

## 5. Open the dashboard

```bash
tiffin login --open
```

## 6. Let your agent help

```bash
claude mcp add tiffin -- tiffin mcp
```

Your agent gets its own API key with full access to all projects, so it can do what you
can. Claude Code asks you before it runs anything destructive (deleting a database, say),
and every change lands in History under the agent's name, ready to undo. For an agent
that should only touch one project, or only read, create a narrower key:
`tiffin tokens create --name ci --projects shop --access read`.

## Run it on a server

The same box runs on a real server. Everything above works the same way; the dashboard
is at `https://dashboard.<ip>.sslip.io` (your server's IP, with dashes) until you give
it a domain.

### Hetzner

Create a read & write API token in the Hetzner Cloud console (your project → Security →
API tokens), then see what you'd get and what it costs, without creating anything:

```bash
export HCLOUD_TOKEN=...
tiffin up --provider hetzner --name shop --dry-run
```

It prints the server, a 40 GB data volume, a firewall and an SSH key, and the monthly
price from Hetzner's own price list. Drop `--dry-run` to create them. The defaults are
a `cax11` (ARM, 2 vCPU, 4 GB) in `fsn1` on Ubuntu 26.04; change them with `--type` and
`--location`. To use your own SSH key instead of one Tiffin
makes, pass `--ssh-key ~/.ssh/id_ed25519` (or set `HCLOUD_SSH_KEY`); only the public
half is uploaded, and a copy already in the project is reused.

Run `tiffin up --name shop` again to update it. To make it bigger in place:

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

Any Ubuntu 26.04 server you can SSH into with passwordless sudo (24.04 also works, for a server you already run):

```bash
tiffin up --provider ssh --name shop --host root@203.0.113.5 --data-disk /dev/sdb
```

`--data-disk` is optional: a blank disk is formatted XFS for your data (one with a
filesystem is used as it is). Without one, data lives on the root disk; that works, but
database branches copy files instead of sharing them unless the disk is XFS.

To make it bigger, resize it at your host (more memory or CPUs, a bigger data disk), then
run `tiffin up --name shop`: it retunes Postgres, Valkey and the memory apps share to the
new machine, and grows an XFS data disk to fill a disk that grew (a partition you grow yourself).

### What Tiffin does to the server

It turns on daily security updates, allows SSH keys only, lets in only SSH, HTTP and
HTTPS, bans brute-force IPs with CrowdSec (never the IP you run `tiffin` from), keeps
the clock in sync, adds a swap file and caps log size. A kernel update never reboots the
server unless you choose a time: `tiffin up --name shop --reboot-window 04:00`.
`tiffin status` shows all of it, including a reboot that is waiting.

### Tiffin's own updates

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
