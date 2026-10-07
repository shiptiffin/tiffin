<template>
  <p class="eyebrow">Nuxt</p>
  <h1>About</h1>
  <p class="lede">This page is prerendered: Nuxt wrote it to HTML at build time (routeRules in nuxt.config.ts).</p>
  <ul class="facts">
    <li><strong>Server.</strong> Nitro's <code>node-server</code> output, which Tiffin starts with <code>.output/server/index.mjs</code>.</li>
    <li><strong>Data.</strong> <code>postgres</code> (postgres.js) reads <code>DATABASE_URL</code> in a server route.</li>
    <li><strong>Forms.</strong> The form is a plain POST to <code>/api/notes</code> that the page's script upgrades to a fetch.</li>
    <li><strong>Assets.</strong> Files under <code>/_nuxt/</code> are named by content hash; the box serves them, cached for a year.</li>
  </ul>
  <p><NuxtLink to="/">Back to the notes</NuxtLink></p>
</template>
