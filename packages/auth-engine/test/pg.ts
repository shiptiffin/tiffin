// A throwaway Postgres for tests: embedded-postgres (MIT) runs pinned PG
// binaries from npm in a temp dir on a free port. One cluster per test
// process; each test file creates its own database.
import EmbeddedPostgres from "embedded-postgres";
import { existsSync, mkdtempSync, readFileSync, rmSync, symlinkSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, relative } from "node:path";
import pg from "pg";

// Bun doesn't run the binaries package's postinstall (it makes the dylib
// symlinks npm tarballs can't carry), so make them here once.
function hydrate() {
  const req = createRequire(createRequire(import.meta.url).resolve("embedded-postgres/package.json"));
  const dir = dirname(req.resolve(`@embedded-postgres/${process.platform}-${process.arch}/package.json`));
  const list = join(dir, "native", "pg-symlinks.json");
  if (!existsSync(list)) return;
  for (const { source, target } of JSON.parse(readFileSync(list, "utf8")) as { source: string; target: string }[]) {
    const t = join(dir, target);
    if (!existsSync(t)) symlinkSync(relative(dirname(t), join(dir, source)), t);
  }
}

let cluster: { pg: EmbeddedPostgres; port: number; dir: string } | null = null;
let starting: Promise<void> | null = null;

async function freePort(): Promise<number> {
  const s = Bun.listen({ hostname: "127.0.0.1", port: 0, socket: { data() {} } });
  const p = s.port;
  s.stop(true);
  return p;
}

async function ensureCluster() {
  if (cluster) return;
  if (!starting) {
    starting = (async () => {
      hydrate();
      const dir = mkdtempSync(join(tmpdir(), "tiffin-auth-pg-"));
      const port = await freePort();
      const e = new EmbeddedPostgres({ databaseDir: join(dir, "data"), user: "postgres", password: "postgres", port, persistent: false, onLog: () => {}, onError: () => {} });
      await e.initialise();
      await e.start();
      cluster = { pg: e, port, dir };
      process.on("exit", () => {
        try {
          rmSync(dir, { recursive: true, force: true });
        } catch {}
      });
    })();
  }
  await starting;
}

/** Creates a fresh database and returns its URL (superuser). */
export async function freshDatabase(name: string): Promise<string> {
  await ensureCluster();
  const admin = new pg.Client({ host: "127.0.0.1", port: cluster!.port, user: "postgres", password: "postgres", database: "postgres" });
  await admin.connect();
  await admin.query(`DROP DATABASE IF EXISTS "${name}"`);
  await admin.query(`CREATE DATABASE "${name}"`);
  await admin.end();
  return `postgres://postgres:postgres@127.0.0.1:${cluster!.port}/${name}`;
}

export async function stopCluster() {
  if (cluster) {
    await cluster.pg.stop();
    rmSync(cluster.dir, { recursive: true, force: true });
    cluster = null;
    starting = null;
  }
}
