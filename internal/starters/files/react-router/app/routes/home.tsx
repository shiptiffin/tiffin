import { Suspense, use, useEffect, useRef } from "react";
import { data, Link, useFetcher } from "react-router";
import { migrate, sql, type Note } from "../db.server";
import type { Route } from "./+types/home";

export const meta: Route.MetaFunction = () => [{ title: "Notes · React Router on Tiffin" }];

// The notes are awaited; the stats are not, so the server sends the page at
// once and streams the stats into it when the query finishes.
export async function loader() {
  await migrate();
  const notes: Note[] = await sql`select id, text, created_at::text from notes order by id desc limit 20`;
  const stats = sql`select count(*)::int as count, split_part(version(), ' ', 2) as version from notes`.then(
    ([row]: { count: number; version: string }[]) => ({ ...row, deploy: process.env.TIFFIN_DEPLOY ?? "local" }),
  );
  return { notes: [...notes], stats };
}

// The form posts here. Without JavaScript it's a plain POST; with it, the
// fetcher submits in place and the loader runs again.
export async function action({ request }: Route.ActionArgs) {
  const text = String((await request.formData()).get("text") ?? "").trim().slice(0, 280);
  if (!text) return data({ error: "Write something first." }, { status: 400 });
  await migrate();
  await sql`insert into notes (text) values (${text})`;
  return { error: null };
}

export default function Home({ loaderData }: Route.ComponentProps) {
  const { notes, stats } = loaderData;
  const fetcher = useFetcher<typeof action>();
  const form = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (fetcher.state === "idle" && fetcher.data && !fetcher.data.error) form.current?.reset();
  }, [fetcher.state, fetcher.data]);

  return (
    <>
      <p className="eyebrow">React Router</p>
      <h1>Notes</h1>
      <p className="lede">
        A loader reads these rows from Postgres on the server; adding one posts to the route's action. <Link to="/about">About this app</Link>
      </p>
      <fetcher.Form method="post" ref={form}>
        <input name="text" maxLength={280} required placeholder="Write something down" aria-label="Note" />
        <button type="submit" disabled={fetcher.state !== "idle"}>
          Add
        </button>
      </fetcher.Form>
      {fetcher.data?.error && <p className="error">{fetcher.data.error}</p>}
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
    </>
  );
}

function Stats({ stats }: { stats: Promise<{ count: number; version: string; deploy: string }> }) {
  const { count, version, deploy } = use(stats);
  return (
    <span>
      {count} {count === 1 ? "note" : "notes"} in Postgres {version} · deploy <code>{deploy}</code>
    </span>
  );
}
