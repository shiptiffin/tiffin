// Written into each build by the Tiffin box for apps that use the Workflow
// DevKit: points its Postgres world at the project's database and brings the
// world's tables up to date (what `bootstrap` of @workflow/world-postgres
// does), one instance at a time.
import { createRequire } from "node:module";
import { dirname, join } from "node:path";

if (!process.env.WORKFLOW_POSTGRES_URL && !process.env.DATABASE_URL) {
  throw new Error(
    "This app uses the Workflow DevKit, which runs on the project's Postgres, and the project has none: add `postgres: {}` to services in tiffin.config.ts, apply, and deploy again.",
  );
}
process.env.WORKFLOW_POSTGRES_URL ||= process.env.DATABASE_URL;

const req = createRequire(join(process.cwd(), "index.js"));
const entry = req.resolve(process.env.WORKFLOW_TARGET_WORLD);
// The world's own dependencies. (Resolved with explicit paths: under Bun,
// Next.js's require hook loses the parent of a createRequire.)
const dep = (id) => req(req.resolve(id, { paths: [dirname(entry)] }));
const { Pool } = dep("pg");
const { drizzle } = dep("drizzle-orm/node-postgres");
const { migrate } = dep("drizzle-orm/node-postgres/migrator");
const { makeWorkerUtils } = dep("graphile-worker");

const pool = new Pool({ connectionString: process.env.WORKFLOW_POSTGRES_URL, max: 3 });
const lock = await pool.connect();
try {
  // Instances of two releases can start at once; their migrations take turns.
  await lock.query("select pg_advisory_lock(hashtext('tiffin:workflow-world-postgres'))");
  await migrate(drizzle(pool), {
    migrationsFolder: join(dirname(entry), "..", "src", "drizzle", "migrations"),
    migrationsTable: "workflow_migrations",
    migrationsSchema: "workflow_drizzle",
  });
  const utils = await makeWorkerUtils({ pgPool: pool });
  try {
    await utils.migrate();
  } finally {
    await utils.release();
  }
  await lock.query("select pg_advisory_unlock(hashtext('tiffin:workflow-world-postgres'))");
} finally {
  lock.release();
  await pool.end();
}
