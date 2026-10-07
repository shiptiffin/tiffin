<svelte:head><title>About · SvelteKit on Tiffin</title></svelte:head>

<p class="eyebrow">SvelteKit</p>
<h1>About</h1>
<p class="lede">This page is prerendered: SvelteKit wrote it to HTML at build time.</p>
<ul class="facts">
  <li><strong>Server.</strong> <code>@sveltejs/adapter-bun</code> builds a <code>Bun.serve</code> server; Tiffin starts it with <code>bun ./build</code>.</li>
  <li><strong>Data.</strong> Bun's built-in Postgres client (<code>Bun.SQL</code>) reads <code>DATABASE_URL</code> through <code>$app/env/private</code>.</li>
  <li><strong>Forms.</strong> The form action is a plain POST, so it works without JavaScript.</li>
  <li><strong>Assets.</strong> Files under <code>/_app/immutable/</code> are named by content hash; the box serves them, cached for a year.</li>
</ul>
<p><a href="/">Back to the notes</a></p>
