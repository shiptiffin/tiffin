# Quickstart

You need a Mac with [Lima](https://lima-vm.io) (`brew install lima`) and the `tiffin` binary.

## 1. Make a box

```bash
tiffin up
```

This creates an Ubuntu VM on your Mac, installs Tiffin and its services, and checks it
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
coding agent knows how to work with the box. Edit the config:

```ts
import { defineConfig } from "tiffin-sdk";

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

Your app is live at `https://web.tiffin.localhost:8443`.

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
a `cax11` (ARM, 2 vCPU, 4 GB) in `fsn1` on Ubuntu 26.04; change them with `--type`,
`--location` and `--image ubuntu-24.04`. To use your own SSH key instead of one Tiffin
makes, pass `--ssh-key ~/.ssh/id_ed25519` (or set `HCLOUD_SSH_KEY`); only the public
half is uploaded, and a copy already in the project is reused.

Run `tiffin up --name shop` again to update it. To delete it:

```bash
tiffin down --confirm shop                 # server, firewall, key; the data volume is kept
tiffin down --confirm shop --delete-data   # the volume too
```

`tiffin down` without `--confirm` shows what would go, and what keeps costing money.

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

Any Ubuntu 24.04 or 26.04 server you can SSH into with passwordless sudo:

```bash
tiffin up --provider ssh --name shop --host root@203.0.113.5 --data-disk /dev/sdb
```

`--data-disk` is optional: a blank disk is formatted XFS for your data (one with a
filesystem is used as it is). Without one, data lives on the root disk; that works, but
database branches copy files instead of sharing them unless the disk is XFS.

### What Tiffin does to the server

It turns on daily security updates, allows SSH keys only, lets in only SSH, HTTP and
HTTPS, bans brute-force IPs with CrowdSec (never the IP you run `tiffin` from), keeps
the clock in sync, adds a swap file and caps log size. A kernel update never reboots the
server unless you choose a time: `tiffin up --name shop --reboot-window 04:00`.
`tiffin status` shows all of it, including a reboot that is waiting.
