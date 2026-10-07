import { createFileRoute, Link, useRouter } from "@tanstack/react-router";
import { Suspense, use, useState, type FormEvent } from "react";
import { addNote, listNotes, noteStats } from "../lib/notes";

// The loader runs on the server for the first request and over HTTP after
// that. The notes are awaited; the stats are not, so the server sends the
// page at once and streams the stats into it when they are ready.
export const Route = createFileRoute("/")({
  loader: async () => {
    const stats = noteStats();
    return { notes: await listNotes(), stats };
  },
  component: Home,
});

function Home() {
  const { notes, stats } = Route.useLoaderData();
  const router = useRouter();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await addNote({ data: text });
      setText("");
      await router.invalidate();
    } finally {
      setBusy(false);
    }
  }

  return (
    <main>
      <p className="eyebrow">TanStack Start</p>
      <h1>Notes</h1>
      <p className="lede">
        A loader reads these rows from Postgres on the server; adding one calls a server function. <Link to="/about">About this app</Link>
      </p>
      <form onSubmit={submit}>
        <input value={text} onChange={(e) => setText(e.target.value)} maxLength={280} required placeholder="Write something down" aria-label="Note" />
        <button type="submit" disabled={busy}>
          Add
        </button>
      </form>
      <ul className="notes">
        {notes.map((n) => (
          <li key={n.id}>
            <span>{n.text}</span>
            <time dateTime={n.created_at}>{n.created_at.slice(0, 16)}</time>
          </li>
        ))}
        {notes.length === 0 && <li className="empty">No notes yet.</li>}
      </ul>
      <footer>
        <Suspense fallback={<span>Counting…</span>}>
          <Stats stats={stats} />
        </Suspense>
      </footer>
    </main>
  );
}

function Stats({ stats }: { stats: ReturnType<typeof noteStats> }) {
  const { count, version, deploy } = use(stats);
  return (
    <span>
      {count} {count === 1 ? "note" : "notes"} in Postgres {version} · deploy <code>{deploy}</code>
    </span>
  );
}
