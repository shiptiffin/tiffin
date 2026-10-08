// The engine's admin API, served on a root-only unix socket and called by the
// Go auth module (reconcile, and the dashboard's Auth pages through the
// platform API). Never reachable from apps or the internet.
import type pg from "pg";
import { SCHEMA } from "./auth";
import { outbox } from "./mail";
import { drop, migrate } from "./migrate";
import type { Registry } from "./registry";

type Json = Record<string, unknown> | unknown[];

const json = (v: Json | null, status = 200) => Response.json(v, { status });
const problem = (status: number, code: string, message: string) => json({ code, message }, status);

const T = (t: string) => `"${SCHEMA}"."${t}"`;

/**
 * A list's page: keyset on ("createdAt", id), newest first, never OFFSET.
 * afterAt/afterId are the last row of the page before (afterAt exactly as
 * this engine sent it: Postgres text, so no precision is lost on the way).
 */
function page(url: URL) {
  const limit = Math.min(Math.max(Number(url.searchParams.get("limit") ?? 50) || 50, 1), 200);
  const search = (url.searchParams.get("search") ?? "").trim();
  const afterAt = url.searchParams.get("afterAt") ?? "";
  const afterId = url.searchParams.get("afterId") ?? "";
  return { limit, search, after: afterAt && afterId ? { at: afterAt, id: afterId } : null };
}

/** Rows fetched with limit+1, as a page: the extra row only says more follow. */
function paged<R extends { _at: string; id: string }>(rows: R[], limit: number) {
  const more = rows.length > limit;
  const kept = rows.slice(0, limit);
  const last = kept[kept.length - 1];
  return { items: kept.map(({ _at: _a, ...r }) => r), next: more && last ? { at: last._at, id: last.id } : null };
}

/** The WHERE for a search and a page position on table alias a; params continue after the given ones. */
function where(a: string, cols: string[], p: ReturnType<typeof page>, params: unknown[]): string {
  const conds: string[] = [];
  if (p.search) {
    params.push(`%${p.search.replace(/[\\%_]/g, (c) => "\\" + c)}%`, p.search);
    const like = `$${params.length - 1}`;
    conds.push(`(${cols.map((c) => `${a}.${c} ILIKE ${like}`).join(" OR ")} OR ${a}.id = $${params.length})`);
  }
  if (p.after) {
    params.push(p.after.at, p.after.id);
    conds.push(`(${a}."createdAt", ${a}.id) < ($${params.length - 1}, $${params.length})`);
  }
  return conds.length ? `WHERE ${conds.join(" AND ")}` : "";
}

async function q<T = Record<string, unknown>>(pool: pg.Pool, text: string, params: unknown[] = []): Promise<T[]> {
  return (await pool.query(text, params)).rows as T[];
}

async function tableExists(pool: pg.Pool, table: string): Promise<boolean> {
  const r = await q<{ ok: boolean }>(pool, `SELECT to_regclass($1) IS NOT NULL AS ok`, [`${SCHEMA}."${table}"`]);
  return !!r[0]?.ok;
}

export function adminHandler(reg: Registry) {
  return async function handle(req: Request): Promise<Response> {
    reg.maybeReload();
    const url = new URL(req.url);
    const parts = url.pathname.split("/").filter(Boolean).map(decodeURIComponent);
    const m = req.method;
    try {
      if (m === "GET" && url.pathname === "/health") return json({ ok: true, projects: reg.projects() });
      if (m === "POST" && url.pathname === "/reload") return json({ projects: reg.reload() });
      if (m === "GET" && url.pathname === "/outbox") {
        const p = url.searchParams.get("project");
        return json(outbox.filter((x) => !p || x.project === p).slice(-50));
      }
      if (parts[0] !== "projects" || !parts[1]) return problem(404, "not_found", "no such admin route");
      const project = parts[1];
      const cfg = reg.projectConfig(project);
      if (!cfg) return problem(404, "not_found", `auth isn't set up for project ${project}`);
      const pool = reg.pool(project)!;
      const rest = parts.slice(2);

      if (m === "POST" && rest[0] === "migrate" && rest.length === 1) {
        const r = await migrate(project, cfg, pool);
        reg.invalidate(project); // Better Auth caches its schema check per instance
        return json(r);
      }
      if (m === "POST" && rest[0] === "drop" && rest.length === 1) {
        if (url.searchParams.get("confirm") !== project) return problem(428, "precondition", `pass ?confirm=${project}: this deletes every user, session and organization`);
        await drop(pool);
        reg.invalidate(project);
        return json({ dropped: true });
      }
      if (m === "GET" && rest[0] === "stats" && rest.length === 1) {
        if (!(await tableExists(pool, "user"))) return json({ users: 0, verifiedUsers: 0, bannedUsers: 0, organizations: 0, activeSessions: 0, signups7d: 0 });
        const [u] = await q<Record<string, string>>(
          pool,
          `SELECT count(*) AS users, count(*) FILTER (WHERE "emailVerified") AS verified, count(*) FILTER (WHERE banned) AS banned,
                  count(*) FILTER (WHERE "createdAt" > now() - interval '7 days') AS signups7d FROM ${T("user")}`,
        );
        const [s] = await q<Record<string, string>>(pool, `SELECT count(*) AS n FROM ${T("session")} WHERE "expiresAt" > now()`);
        const orgs = cfg.organizations && (await tableExists(pool, "organization")) ? (await q<{ n: string }>(pool, `SELECT count(*) AS n FROM ${T("organization")}`))[0]?.n : "0";
        return json({
          users: Number(u?.users ?? 0),
          verifiedUsers: Number(u?.verified ?? 0),
          bannedUsers: Number(u?.banned ?? 0),
          signups7d: Number(u?.signups7d ?? 0),
          activeSessions: Number(s?.n ?? 0),
          organizations: Number(orgs ?? 0),
        });
      }
      if (rest[0] === "users") {
        if (m === "GET" && rest.length === 1) {
          const p = page(url);
          const params: unknown[] = [p.limit + 1];
          const rows = await q<{ _at: string; id: string }>(
            pool,
            `SELECT u.id, u.email, u.name, u.image, u."emailVerified", u.banned, u."banReason", u."banExpires", u."createdAt", u."updatedAt",
                    (SELECT max(s."updatedAt") FROM ${T("session")} s WHERE s."userId" = u.id) AS "lastSeenAt",
                    u."createdAt"::text AS _at
               FROM ${T("user")} u ${where("u", ["email", "name"], p, params)}
              ORDER BY u."createdAt" DESC, u.id DESC LIMIT $1`,
            params,
          );
          const { items, next } = paged(rows, p.limit);
          return json({ users: items, next });
        }
        const id = rest[1]!;
        const [user] = await q(
          pool,
          `SELECT id, email, name, image, "emailVerified", banned, "banReason", "banExpires", "createdAt", "updatedAt", "twoFactorEnabled" FROM ${T("user")} WHERE id = $1`,
          [id],
        );
        if (!user) return problem(404, "not_found", `no user ${id} in project ${project}`);
        if (m === "GET" && rest.length === 2) {
          // Independent reads: at once, within the project's small pool.
          const [accounts, sessions, memberships, passkeys, apiKeys] = await Promise.all([
            q(pool, `SELECT "providerId", "accountId", "createdAt" FROM ${T("account")} WHERE "userId" = $1 ORDER BY "createdAt"`, [id]),
            q(
              pool,
              `SELECT id, "ipAddress", "userAgent", "createdAt", "updatedAt", "expiresAt" FROM ${T("session")} WHERE "userId" = $1 AND "expiresAt" > now() ORDER BY "updatedAt" DESC LIMIT 50`,
              [id],
            ),
            (async () =>
              cfg.organizations && (await tableExists(pool, "member"))
                ? q(
                    pool,
                    `SELECT o.id AS "organizationId", o.name, o.slug, m.role, m."createdAt" FROM ${T("member")} m JOIN ${T("organization")} o ON o.id = m."organizationId" WHERE m."userId" = $1 ORDER BY m."createdAt"`,
                    [id],
                  )
                : [])(),
            (async () =>
              (await tableExists(pool, "passkey")) ? Number((await q<{ n: string }>(pool, `SELECT count(*) AS n FROM ${T("passkey")} WHERE "userId" = $1`, [id]))[0]?.n ?? 0) : 0)(),
            (async () =>
              (await tableExists(pool, "apikey"))
                ? q(pool, `SELECT id, name, start, prefix, enabled, "expiresAt", "createdAt", "lastRequest", metadata FROM ${T("apikey")} WHERE "referenceId" = $1 ORDER BY "createdAt" DESC`, [id])
                : [])(),
          ]);
          return json({ user, accounts, sessions, memberships, passkeys, apiKeys });
        }
        if (m === "POST" && rest[2] === "ban" && rest.length === 3) {
          const body = (await req.json().catch(() => ({}))) as { reason?: string; expiresAt?: string | null };
          const exp = body.expiresAt ? new Date(body.expiresAt) : null;
          if (exp && Number.isNaN(exp.getTime())) return problem(422, "validation", "expiresAt must be an ISO date");
          await q(pool, `UPDATE ${T("user")} SET banned = true, "banReason" = $2, "banExpires" = $3, "updatedAt" = now() WHERE id = $1`, [id, body.reason ?? null, exp]);
          const r = await pool.query(`DELETE FROM ${T("session")} WHERE "userId" = $1`, [id]);
          return json({ banned: true, sessionsRevoked: r.rowCount ?? 0 });
        }
        if (m === "POST" && rest[2] === "unban" && rest.length === 3) {
          await q(pool, `UPDATE ${T("user")} SET banned = false, "banReason" = NULL, "banExpires" = NULL, "updatedAt" = now() WHERE id = $1`, [id]);
          return json({ banned: false });
        }
        if (m === "POST" && rest[2] === "revoke-sessions" && rest.length === 3) {
          const r = await pool.query(`DELETE FROM ${T("session")} WHERE "userId" = $1`, [id]);
          return json({ sessionsRevoked: r.rowCount ?? 0 });
        }
      }
      if (rest[0] === "orgs") {
        if (!cfg.organizations || !(await tableExists(pool, "organization"))) {
          if (m === "GET" && rest.length === 1) return json({ organizations: [], next: null });
          return problem(404, "not_found", "organizations are off for this project");
        }
        if (m === "GET" && rest.length === 1) {
          const p = page(url);
          const params: unknown[] = [p.limit + 1];
          const rows = await q<{ _at: string; id: string }>(
            pool,
            `SELECT o.id, o.name, o.slug, o.logo, o."createdAt", o.metadata,
                    (SELECT count(*) FROM ${T("member")} m WHERE m."organizationId" = o.id)::int AS "memberCount",
                    (SELECT count(*) FROM ${T("invitation")} i WHERE i."organizationId" = o.id AND i.status = 'pending')::int AS "pendingInvitations",
                    o."createdAt"::text AS _at
               FROM ${T("organization")} o ${where("o", ["name", "slug"], p, params)}
              ORDER BY o."createdAt" DESC, o.id DESC LIMIT $1`,
            params,
          );
          const { items, next } = paged(rows, p.limit);
          return json({ organizations: items, next });
        }
        if (m === "GET" && rest.length === 2) {
          const id = rest[1]!;
          const [org] = await q(pool, `SELECT id, name, slug, logo, "createdAt", metadata FROM ${T("organization")} WHERE id = $1`, [id]);
          if (!org) return problem(404, "not_found", `no organization ${id} in project ${project}`);
          const members = await q(
            pool,
            `SELECT m.id, m."userId", m.role, m."createdAt", u.email, u.name, u.image FROM ${T("member")} m JOIN ${T("user")} u ON u.id = m."userId" WHERE m."organizationId" = $1 ORDER BY m."createdAt"`,
            [id],
          );
          const invitations = await q(
            pool,
            `SELECT id, email, role, status, "expiresAt", "inviterId", "createdAt" FROM ${T("invitation")} WHERE "organizationId" = $1 ORDER BY "createdAt" DESC LIMIT 100`,
            [id],
          );
          const links = (await tableExists(pool, "orgInviteLink"))
            ? await q(
                pool,
                `SELECT id, role, "maxUses", uses, "expiresAt", "createdBy", "createdAt", "revokedAt" FROM ${T("orgInviteLink")} WHERE "organizationId" = $1 ORDER BY "createdAt" DESC LIMIT 100`,
                [id],
              )
            : [];
          return json({ organization: org, members, invitations, inviteLinks: links });
        }
      }
      return problem(404, "not_found", "no such admin route");
    } catch (err) {
      console.error(JSON.stringify({ level: "error", msg: "admin request failed", path: url.pathname, err: String(err) }));
      return problem(500, "internal", String(err instanceof Error ? err.message : err));
    }
  };
}
