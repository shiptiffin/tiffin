# hello-hono

A Hono API on Bun, deployed to a Tiffin box.

```bash
tiffin plan && tiffin apply --confirm <hash>   # creates project "hello" with app "api"
tiffin deploy                                   # builds on the box, prints the URL
curl https://hello.tiffin.localhost:8443/       # the project's name (your box's port)
tiffin logs api -f                              # every request is logged
tiffin secrets set hello GREETING --value hi    # restarts the app with the new env
```

Run it locally with `bun install && bun run dev`.
