# hello-next

A Next.js 16 app (app router) running on Bun on a Tiffin box, with two instances that
share one cache in Valkey.

```bash
tiffin plan && tiffin apply --confirm <hash>   # project "hello-next": app "web" + Valkey
tiffin deploy                                   # next build on the box, then live
curl https://web.tiffin.localhost:8443/         # "Cached at ..." is the same on both instances
curl -X POST https://web.tiffin.localhost:8443/api/revalidate   # new value, on both
```

- `cache-handler.mjs` is `tiffin-sdk/next/cache-handler`, bundled (`bun run sync-cache-handler`).
  See packages/sdk/src/next/README.md for how the cache works.
- `next build` on a 4 GB box takes about a minute and peaks around 1.5 GB; builds run in a
  memory-capped cgroup with swap. Short on memory? Build elsewhere and `tiffin deploy --prebuilt image.tar`.
- The start script is `bun --bun next start`: Next.js runs on Bun 1.4.2+.
