# ShipTiffin

> ShipTiffin runs all your apps on one Linux server you own (a box), with a database, auth, KV, file storage, email, jobs, analytics, error tracking and backups already on it. People and their coding agents run it through one CLI (`tiffin`), one MCP server and one dashboard. A managed box is $19 a month per box plus the server at Hetzner; running Tiffin yourself is free (AGPL-3.0).

## If you are an agent setting this up for someone

Follow https://shiptiffin.com/agent-setup.md. Some steps only the person can do (signing in, paying, Hetzner credentials, adding a passkey, creating the first API key): stop and ask them at those points. Never ask for passwords or card details.

## Price

- Managed: $19 a month per box ($12 for the first 100 customers, locked for 24 months), monthly, 14-day money-back. No usage charges, seats or per-project fees.
- The server is billed by Hetzner to the person, at Hetzner's prices: about $10 a month before VAT for the smallest (2 vCPU, 4 GB, IPv4, 40 GB data volume).
- Self-hosted: free. The software is open source (AGPL-3.0) at https://github.com/shiptiffin/tiffin.

## What is on a box

Apps (Next.js first; Hono, FastAPI, TanStack Start, SvelteKit, Nuxt, React Router, Astro, static sites, Dockerfiles), a preview per pull request, Postgres 18 per project, Redis-compatible KV, S3-compatible files, sign-in (Better Auth), email through the person's own provider, jobs, cron and workflows, cookieless analytics, Sentry-compatible error tracking, logs and metrics, backups with point-in-time restore (7 days), domains with HTTPS. Each project can have a hard limit on CPU and memory.

## Operating a box

Every box serves an MCP server at `https://dashboard.<box domain>/mcp` (an API key from Settings › API keys as `Authorization: Bearer <key>`) and its API at `https://dashboard.<box domain>/v1/openapi.json`. Change a box only by plan, then apply with the plan's hash.

## More

- [Docs index](https://shiptiffin.com/llms.txt) and [all docs in one file](https://shiptiffin.com/llms-full.txt)
- [Pricing](https://shiptiffin.com/pricing), [compared with Vercel, Supabase and Coolify](https://shiptiffin.com/compare), [security](https://shiptiffin.com/security)
