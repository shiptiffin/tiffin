// The early-access list in the project's Postgres: one table, early_access,
// which the owner reads in the dashboard's Database browser. It is created on
// first use (create table if not exists), so a fresh database needs nothing.
import postgres from "postgres";
import { decide, type Answers, type Repo, type Signup } from "./early-access";

type Sql = postgres.Sql;

const SCHEMA = `
create table if not exists early_access (
  id bigint generated always as identity primary key,
  email text not null unique check (email = lower(email)),
  hosting text,
  projects text,
  current_hosts text[] not null default '{}',
  note text,
  status text not null default 'pending' check (status in ('pending', 'confirmed')),
  token_hash text not null unique,
  submissions integer not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  confirm_sent_at timestamptz,
  confirmed_at timestamptz
);
comment on table early_access is 'shiptiffin.com early-access list. One row per address (lower-cased); status pending until the address is confirmed by email. Removing someone: delete the row.';
comment on column early_access.hosting is 'What would you host?';
comment on column early_access.projects is 'How many projects? 1, 2-5, 6-20 or more';
comment on column early_access.current_hosts is 'What do you use today? vercel, railway, render, fly, vps, other';
comment on column early_access.token_hash is 'sha256 of the token in the confirm and remove links';
comment on column early_access.submissions is 'How many times the form was sent for this address';
`;

type Row = {
  id: string | number;
  email: string;
  hosting: string | null;
  projects: string | null;
  current_hosts: string[];
  note: string | null;
  status: "pending" | "confirmed";
  confirm_sent_at: Date | null;
  created_at: Date;
};

const toSignup = (r: Row): Signup => ({
  id: Number(r.id),
  email: r.email,
  hosting: r.hosting,
  projects: r.projects,
  currentHosts: r.current_hosts ?? [],
  note: r.note,
  status: r.status,
  confirmSentAt: r.confirm_sent_at,
  createdAt: r.created_at,
});

let client: Sql | null = null;
let ready: Promise<void> | null = null;

/** The shared client, or null when the project has no database (DATABASE_URL unset). */
export function sql(): Sql | null {
  const url = process.env.DATABASE_URL;
  if (!url) return null;
  // prepare: false, as everywhere on the box: DATABASE_URL goes through PgBouncer.
  client ??= postgres(url, {
    prepare: false,
    max: Number(process.env.DATABASE_POOL_MAX) || 5,
    idle_timeout: 30,
    connect_timeout: 10,
    onnotice: () => {},
  });
  return client;
}

async function ensure(db: Sql): Promise<void> {
  ready ??= db.unsafe(SCHEMA).simple().then(
    () => undefined,
    async (err) => {
      ready = null;
      // Two instances creating it at once: one wins, the other sees it there.
      const [{ ok }] = await db<{ ok: boolean }[]>`select to_regclass('public.early_access') is not null as ok`;
      if (!ok) throw err;
    },
  );
  return ready;
}

export function pgRepo(db: Sql): Repo {
  return {
    async register(a: Answers, token, now) {
      await ensure(db);
      // Addresses nobody confirmed go after 30 days (the privacy policy says so).
      await db`delete from early_access where status = 'pending' and created_at < ${now}::timestamptz - interval '30 days'`;
      try {
        return await db.begin(async (tx) => {
          const [row] = await tx<Row[]>`select * from early_access where email = ${a.email} for update`;
          const what = decide(row ? toSignup(row) : null, now);
          if (what === "new") {
            await tx`
              insert into early_access (email, hosting, projects, current_hosts, note, token_hash, confirm_sent_at)
              values (${a.email}, ${a.hosting}, ${a.projects}, ${a.currentHosts}, ${a.note}, ${token.hash}, ${now})`;
          } else if (row!.status === "pending") {
            // Not confirmed yet: the newest answers win.
            await tx`
              update early_access set
                hosting = coalesce(${a.hosting}, hosting),
                projects = coalesce(${a.projects}, projects),
                current_hosts = case when cardinality(${a.currentHosts}::text[]) > 0 then ${a.currentHosts}::text[] else current_hosts end,
                note = coalesce(${a.note}, note),
                submissions = submissions + 1,
                updated_at = ${now}
                ${what === "resend" ? tx`, token_hash = ${token.hash}, confirm_sent_at = ${now}` : tx``}
              where id = ${row!.id}`;
          } else {
            await tx`update early_access set submissions = submissions + 1, updated_at = ${now} where id = ${row!.id}`;
          }
          return what;
        });
      } catch (err) {
        // The same address sent twice at the same moment: the other request has it.
        if ((err as { code?: string }).code === "23505") return "quiet";
        throw err;
      }
    },

    async confirm(hash, now) {
      await ensure(db);
      const [row] = await db<(Row & { was: string })[]>`
        update early_access e set
          status = 'confirmed',
          confirmed_at = coalesce(e.confirmed_at, ${now}),
          updated_at = ${now}
        from (select id, status as was from early_access where token_hash = ${hash} for update) old
        where e.id = old.id
        returning e.*, old.was`;
      return row ? { signup: toSignup(row), first: row.was === "pending" } : null;
    },

    async remove(hash) {
      await ensure(db);
      const rows = await db`delete from early_access where token_hash = ${hash} returning id`;
      return rows.length > 0;
    },
  };
}
