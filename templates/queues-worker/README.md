# queues-worker

A Bun worker for Tiffin's queues, cron and durable workflows.

```bash
tiffin plan && tiffin apply --confirm <hash>       # project "jobs": a worker app and a cron
tiffin deploy                                       # builds on the box
tiffin queue configure jobs work --app worker --key-concurrency 2
tiffin queue send jobs --name work --body '{"payload":{"ms":2000},"key":"tenant-1"}'
tiffin queue stats jobs                              # depth, running, dead, p95
tiffin queue jobs list jobs --state dead             # the dead-letter queue
tiffin workflows start jobs --workflow nap --app worker --body '{"input":{"sleep":"5m"}}'
tiffin workflows runs get jobs <run id>              # steps and timeline
tiffin queue crons list jobs                         # next tick of "tick"
```

`tiffin-sdk.gen.js` is tiffin-sdk/queue + tiffin-sdk/workflow bundled
(`bun run sync-sdk`) until tiffin-sdk is published.
