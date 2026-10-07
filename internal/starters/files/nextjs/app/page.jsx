import { revalidatePath } from "next/cache";
import { db, migrate } from "../lib/db.js";

// Rendered on every request, straight from Postgres on the same box.
export const dynamic = "force-dynamic";

async function addNote(form) {
  "use server";
  const text = String(form.get("text") ?? "").trim().slice(0, 280);
  if (!text) return;
  await migrate();
  await db()`insert into notes (text) values (${text})`;
  revalidatePath("/");
}

export default async function Home() {
  await migrate();
  const sql = db();
  const [notes, [{ count, version }]] = await Promise.all([
    sql`select id, text, created_at from notes order by id desc limit 20`,
    sql`select count(*)::int as count, split_part(version(), ' ', 2) as version from notes`,
  ]);
  return (
    <main>
      <p className="eyebrow">Next.js · Postgres {version}</p>
      <h1>Notes</h1>
      <p className="lede">
        A server component reads these rows on every request; the form is a server action. {count}{" "}
        {count === 1 ? "note" : "notes"} so far.
      </p>
      <form action={addNote}>
        <input name="text" maxLength={280} required placeholder="Write something down" aria-label="Note" />
        <button type="submit">Add</button>
      </form>
      <ul className="notes">
        {notes.map((n) => (
          <li key={n.id}>
            <span>{n.text}</span>
            <time dateTime={new Date(n.created_at).toISOString()}>
              {new Date(n.created_at).toISOString().slice(0, 16).replace("T", " ")}
            </time>
          </li>
        ))}
        {notes.length === 0 && <li className="empty">No notes yet.</li>}
      </ul>
      <footer>
        Deploy <code>{process.env.TIFFIN_DEPLOY ?? "local"}</code>, instance{" "}
        <code>{process.env.TIFFIN_INSTANCE ?? "0"}</code>
      </footer>
    </main>
  );
}
