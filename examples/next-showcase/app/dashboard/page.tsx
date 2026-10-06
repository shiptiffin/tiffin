import type { Metadata } from "next";
import { connection } from "next/server";
import { Suspense } from "react";
import { POSTS } from "@/lib/content";
import { sql } from "@/lib/db";
import { editPost, restock } from "../actions";
import { LikeButton } from "./like-button";
import { NotesBoard } from "./notes-board";

export const metadata: Metadata = { title: "Dashboard" };

// Dynamic: every section queries Postgres per request and streams in on
// its own. "Recent orders" is slow on purpose (?delay=ms, default 1000).
export default function Dashboard({ searchParams }: PageProps<"/dashboard">) {
  return (
    <main>
      <h1 id="dash-shell">Dashboard</h1>
      <p className="muted">Each section below streams in when its query finishes.</p>
      <Suspense fallback={<div className="skeleton" id="stats-loading" />}>
        <Stats />
      </Suspense>
      <div className="split">
        <section>
          <h2>Recent orders</h2>
          <Suspense fallback={<div className="skeleton" id="orders-loading" />}>
            <RecentOrders searchParams={searchParams} />
          </Suspense>
        </section>
        <section>
          <h2>Notes</h2>
          <Suspense fallback={<div className="skeleton" id="notes-loading" />}>
            <Notes />
          </Suspense>
        </section>
      </div>
      <h2>Most liked</h2>
      <Suspense fallback={<div className="skeleton" />}>
        <Likes />
      </Suspense>
      <h2>Admin</h2>
      <div className="row" style={{ justifyContent: "flex-start", flexWrap: "wrap" }}>
        {POSTS.map((p) => (
          <form key={p.slug} action={editPost}>
            <input type="hidden" name="slug" value={p.slug} />
            <button className="ghost" id={`edit-${p.slug}`}>
              Edit “{p.slug}”
            </button>
          </form>
        ))}
        <form action={restock}>
          <button className="ghost" id="restock">
            Restock all
          </button>
        </form>
      </div>
    </main>
  );
}

async function Stats() {
  await connection();
  const [s] = await sql()<{ orders: number; units: number; revenue: number; notes: number }[]>`
    select (select count(*)::int from orders) as orders,
           (select coalesce(sum(qty), 0)::int from orders) as units,
           (select coalesce(sum(o.qty * p.price), 0)::int from orders o join products p on p.id = o.product_id) as revenue,
           (select count(*)::int from notes) as notes`;
  return (
    <div className="grid" id="stats">
      <div className="card">
        <strong>{s!.orders}</strong> orders
      </div>
      <div className="card">
        <strong>{s!.units}</strong> units
      </div>
      <div className="card">
        <strong>${(s!.revenue / 100).toFixed(0)}</strong> revenue
      </div>
      <div className="card">
        <strong>{s!.notes}</strong> notes
      </div>
    </div>
  );
}

async function RecentOrders({ searchParams }: { searchParams: PageProps<"/dashboard">["searchParams"] }) {
  const sp = await searchParams;
  const delay = Math.min(Math.max(Number(sp.delay ?? 1000) || 0, 0), 5000) / 1000;
  if (delay > 0) await sql()`select pg_sleep(${delay})`;
  const rows = await sql()<{ id: number; name: string; qty: number; created_at: Date }[]>`
    select o.id, p.name, o.qty, o.created_at
    from orders o join products p on p.id = o.product_id
    order by o.created_at desc limit 8`;
  return (
    <ul className="plain" id="orders" data-loaded="true">
      {rows.map((r) => (
        <li key={r.id} className="row">
          <span>
            {r.qty} × {r.name}
          </span>
          <span className="muted">#{r.id}</span>
        </li>
      ))}
    </ul>
  );
}

async function Notes() {
  await connection();
  const notes = await sql()<{ id: number; text: string }[]>`select id, text from notes order by id desc limit 10`;
  return <NotesBoard notes={[...notes]} />;
}

async function Likes() {
  await connection();
  const rows = await sql()<{ id: number; name: string; likes: number }[]>`
    select id, name, likes from products order by likes desc, id limit 4`;
  return (
    <div className="grid">
      {rows.map((p) => (
        <div className="card row" key={p.id}>
          <span>{p.name}</span>
          <LikeButton id={p.id} likes={p.likes} />
        </div>
      ))}
    </div>
  );
}
