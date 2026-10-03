# static-site

Plain files served by the Tiffin edge over HTTPS, with no container.

```bash
tiffin plan && tiffin apply --confirm <hash>
tiffin deploy                  # → https://site.<your box domain>
tiffin deploy --preview draft  # → https://draft--site.<your box domain>
```

What gets served: `Staticfile`'s `root:` if present, else the first of `dist/`, `build/`,
`out/`, `public/` or the app directory that has an `index.html`. Add a `build` script to
`package.json` (Vite, Astro, ...) and the box runs `bun install && bun run build` first.
Set `index_fallback: true` in a `Staticfile` for single-page apps.
