// Nuxt 4 on Tiffin: Nitro's node-server output (the box pins it), started
// with `.output/server/index.mjs`.
export default defineNuxtConfig({
  compatibilityDate: "2026-10-01",
  devtools: { enabled: false },
  css: ["~/assets/styles.css"],
  app: {
    head: {
      title: "Notes · Nuxt on Tiffin",
      link: [{ rel: "icon", type: "image/svg+xml", href: "/favicon.svg" }],
    },
  },
  routeRules: {
    // Written to HTML at build time.
    "/about": { prerender: true },
  },
});
