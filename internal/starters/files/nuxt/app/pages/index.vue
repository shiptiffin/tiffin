<script setup lang="ts">
// Rendered on the server: useFetch runs there for the first request and the
// page arrives with the notes in it.
const { data, refresh } = await useFetch("/api/notes");
const text = ref("");
const busy = ref(false);
const error = ref("");

async function add() {
  busy.value = true;
  error.value = "";
  try {
    await $fetch("/api/notes", { method: "POST", body: { text: text.value } });
    text.value = "";
    await refresh();
  } catch (e: any) {
    error.value = e?.statusMessage ?? "Could not add the note.";
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <p class="eyebrow">Nuxt</p>
  <h1>Notes</h1>
  <p class="lede">A server route reads these rows from Postgres; adding one posts to it. <NuxtLink to="/about">About this app</NuxtLink></p>
  <form method="post" action="/api/notes" @submit.prevent="add">
    <input v-model="text" name="text" maxlength="280" required placeholder="Write something down" aria-label="Note" />
    <button type="submit" :disabled="busy">Add</button>
  </form>
  <p v-if="error" class="error">{{ error }}</p>
  <ul class="notes">
    <li v-for="n in data?.notes" :key="n.id">
      <span>{{ n.text }}</span><time :datetime="n.created_at">{{ n.created_at.slice(0, 16) }}</time>
    </li>
    <li v-if="!data?.notes.length" class="empty">No notes yet.</li>
  </ul>
  <footer v-if="data">
    {{ data.count }} {{ data.count === 1 ? "note" : "notes" }} in Postgres {{ data.version }} · deploy <code>{{ data.deploy }}</code>
  </footer>
</template>
