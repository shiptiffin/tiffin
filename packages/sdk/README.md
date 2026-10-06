# @shiptiffin/sdk

The SDK for apps on [Tiffin](https://github.com/shiptiffin/tiffin), the agent-first app-in-a-box
platform. ESM only; import the part you use.

```sh
bun add @shiptiffin/sdk     # or npm install @shiptiffin/sdk
```

| Import | What it is |
| --- | --- |
| `@shiptiffin/sdk` | `defineConfig` and the types for `tiffin.config.ts` |
| `@shiptiffin/sdk/kv` | the project's Valkey namespace: get/set, JSON, counters, rate limits |
| `@shiptiffin/sdk/storage` | buckets: upload, signed and public URLs, upload tickets and routes |
| `@shiptiffin/sdk/queue`, `/workflow` | background jobs and durable workflows |
| `@shiptiffin/sdk/verify` | check that a delivery came from the box |
| `@shiptiffin/sdk/auth` | sessions and roles on the server, verified locally |
| `@shiptiffin/sdk/email` | send email through the box |
| `@shiptiffin/sdk/analytics`, `/vitals` | events and Web Vitals |
| `@shiptiffin/sdk/client` | browser helpers, no framework: `uploadFile`, `subscribeRun`, the bot check |
| `@shiptiffin/sdk/next/*` | Next.js: `auth`, `cache-handler`, `use-cache`, `image-loader`, `vitals` |

Sign-in and sign-up forms are your own, built on Better Auth's client (`better-auth/client` or
`better-auth/react`) pointed at the box's `/api/auth`. The guides are in
[docs/guide](https://github.com/shiptiffin/tiffin/tree/main/docs/guide).

No registry? `tiffin sdk add` vendors the copy that ships inside the `tiffin` binary.
