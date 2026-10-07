// Creates and upgrades a project's `tiffin_auth` schema: Better Auth's tables for
// the enabled plugins, plus the org_id helpers apps use for row-level
// security. Idempotent: run it on every apply.
import { getMigrations } from "better-auth/db/migration";
import type pg from "pg";
import { buildOptions, SCHEMA } from "./auth";
import type { ProjectConfig } from "./config";

export const HELPERS_SQL = `
CREATE SCHEMA IF NOT EXISTS ${SCHEMA};

-- The organization (and user) the current transaction acts for. Apps set them
-- with set_config('app.org_id', <id>, true); @shiptiffin/sdk/auth's withOrg does it.
CREATE OR REPLACE FUNCTION ${SCHEMA}.org_id() RETURNS text
  LANGUAGE sql STABLE PARALLEL SAFE
  AS $$ SELECT nullif(current_setting('app.org_id', true), '') $$;
CREATE OR REPLACE FUNCTION ${SCHEMA}.user_id() RETURNS text
  LANGUAGE sql STABLE PARALLEL SAFE
  AS $$ SELECT nullif(current_setting('app.user_id', true), '') $$;

-- Turn on org isolation for a table: rows are visible and writable only when
-- their org column equals app.org_id. FORCE applies it to the table owner too.
-- (Superusers and BYPASSRLS roles still bypass it: don't run apps as those.)
CREATE OR REPLACE FUNCTION ${SCHEMA}.enable_org_rls(tbl regclass, col name DEFAULT 'org_id') RETURNS void
  LANGUAGE plpgsql
  AS $$
BEGIN
  EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', tbl);
  EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', tbl);
  EXECUTE format('DROP POLICY IF EXISTS tiffin_org_isolation ON %s', tbl);
  EXECUTE format('CREATE POLICY tiffin_org_isolation ON %s USING (%I = ${SCHEMA}.org_id()) WITH CHECK (%I = ${SCHEMA}.org_id())', tbl, col, col);
END $$;

GRANT USAGE ON SCHEMA ${SCHEMA} TO PUBLIC;
GRANT EXECUTE ON FUNCTION ${SCHEMA}.org_id(), ${SCHEMA}.user_id() TO PUBLIC;
`;

export type MigrateResult = { created: string[]; added: string[]; ms: number };

export async function migrate(project: string, c: ProjectConfig, pool: pg.Pool): Promise<MigrateResult> {
  const t0 = performance.now();
  await pool.query(HELPERS_SQL);
  const m = await getMigrations(buildOptions(project, c, pool));
  if (m.unsafeChanges.length) throw new Error("auth schema migration refused: " + m.unsafeChanges.join("; "));
  await m.runMigrations();
  // One membership per person per organization: concurrent joins (invite
  // links, invitations) can't leave a duplicate that outlives a removal.
  if (c.organizations) {
    await pool.query(`CREATE UNIQUE INDEX IF NOT EXISTS member_org_user ON ${SCHEMA}."member" ("organizationId", "userId")`);
  }
  return {
    created: m.toBeCreated.map((t) => t.table),
    added: m.toBeAdded.flatMap((t) => Object.keys(t.fields).map((f) => `${t.table}.${f}`)),
    ms: Math.round(performance.now() - t0),
  };
}

/** Irreversible: removes every user, session and organization of the project. */
export async function drop(pool: pg.Pool): Promise<void> {
  await pool.query(`DROP SCHEMA IF EXISTS ${SCHEMA} CASCADE`);
}
