// The test fixture: a static-first site with a few on-demand routes and no
// adapter, so tiffin() has to add @astrojs/node itself. It imports the built
// package (run `bun run build` in packages/astro first).
import { defineConfig } from "astro/config";
import tiffin from "../../dist/index.js";

export default defineConfig({
  integrations: [tiffin({ shutdown: { timeout: 5 } })],
});
