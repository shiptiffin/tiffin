// The release command: runs with DATABASE_URL straight to Postgres, so a
// session advisory lock works (through a transaction pooler it would not).
import { sql } from "bun";

const [{ port }] = await sql`select current_setting('port') as port`;
await sql`select pg_advisory_lock(42)`;
await sql`create table if not exists notes (id serial primary key, body text)`;
await sql`insert into notes (body) select 'note ' || g from generate_series(1, 20) g where not exists (select 1 from notes)`;
await sql`select pg_advisory_unlock(42)`;
console.log(`migrated through port ${process.env.PGPORT} (server port ${port})`);
