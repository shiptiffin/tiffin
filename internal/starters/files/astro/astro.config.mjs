// @ts-check
import { defineConfig, fontProviders } from "astro/config";

// Static by default: `astro build` renders every page to HTML in dist/, and the
// box's edge serves it. Pages ship no JavaScript unless a component asks for
// it with a client:* directive (an island).
// https://docs.astro.build/en/reference/configuration-reference/
export default defineConfig({
  // The Fonts API downloads the font at build time and serves it from this
  // site, with a matched fallback so text doesn't jump while it loads.
  fonts: [
    {
      provider: fontProviders.fontsource(),
      name: "Newsreader",
      cssVariable: "--font-display",
      weights: [600],
      styles: ["normal"],
      subsets: ["latin"],
      fallbacks: ["Georgia", "serif"],
    },
  ],
});
