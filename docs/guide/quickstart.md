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

Agents get their own token: they can plan anything and apply reversible changes. For
anything that deletes data or makes it public, they send you an approval link and
wait for your passkey.
