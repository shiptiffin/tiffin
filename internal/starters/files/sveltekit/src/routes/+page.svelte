<script lang="ts">
  import { enhance } from "$app/forms";
  let { data, form } = $props();
</script>

<svelte:head><title>Notes · SvelteKit on Tiffin</title></svelte:head>

<p class="eyebrow">SvelteKit</p>
<h1>Notes</h1>
<p class="lede">
  A server <code>load</code> reads these rows from Postgres; adding one posts a form action. <a href="/about">About this app</a>
</p>
<form method="POST" action="?/add" use:enhance={() => async ({ update }) => update({ reset: true })}>
  <input name="text" maxlength="280" required placeholder="Write something down" aria-label="Note" />
  <button type="submit">Add</button>
</form>
{#if form?.error}<p class="error">{form.error}</p>{/if}
<ul class="notes">
  {#each data.notes as n (n.id)}
    <li><span>{n.text}</span><time datetime={n.created_at}>{n.created_at.slice(0, 16)}</time></li>
  {:else}
    <li class="empty">No notes yet.</li>
  {/each}
</ul>
<footer>
  {#await data.stats}
    <span>Counting…</span>
  {:then s}
    <span>{s.count} {s.count === 1 ? "note" : "notes"} in Postgres {s.version} · deploy <code>{s.deploy}</code></span>
  {/await}
</footer>
