// The public side: the edge sends /api/auth/* for every web app host of an
// auth-enabled project here, keeping the Host header. Host picks the project.
// Apps calling from inside the box can name the host in x-tiffin-host.
import { requestFacts, type RequestFacts } from "./context";
import type { Registry } from "./registry";

const problem = (status: number, code: string, message: string) => Response.json({ code, message }, { status });

export function publicHandler(reg: Registry) {
  return async function handle(req: Request): Promise<Response> {
    reg.maybeReload();
    const url = new URL(req.url);
    if (url.pathname === "/healthz") return Response.json({ ok: true });
    if (!url.pathname.startsWith("/api/auth")) return problem(404, "not_found", "The auth engine only serves /api/auth.");
    const project = reg.projectForHost(req.headers.get("host")) ?? reg.projectForHost(req.headers.get("x-tiffin-host"));
    if (!project) return problem(404, "unknown_host", "No auth-enabled project serves this host. Turn on services.auth in tiffin.config.ts and apply.");
    const inst = reg.get(project);
    if (!inst) return problem(404, "unknown_host", "No auth-enabled project serves this host.");

    const facts: RequestFacts = {};
    const key = req.headers.get("x-api-key");
    if (key) {
      // Learn who the key belongs to and its role cap before Better Auth runs,
      // so org hooks can hold the key to (at most) its sponsor's role.
      try {
        const r = (await (inst.auth.api as unknown as { verifyApiKey: (a: unknown) => Promise<unknown> }).verifyApiKey({ body: { key } } as never)) as {
          valid: boolean;
          key: { id: string; referenceId?: string; userId?: string; metadata?: Record<string, unknown> | null } | null;
        };
        if (r?.valid && r.key) {
          const userId = r.key.referenceId ?? r.key.userId ?? "";
          const banned = await inst.pool.query(`SELECT banned, "banExpires" FROM auth."user" WHERE id = $1`, [userId]);
          const row = banned.rows[0] as { banned?: boolean; banExpires?: Date | null } | undefined;
          if (row?.banned && (!row.banExpires || new Date(row.banExpires) > new Date())) {
            return problem(403, "BANNED", "This account is suspended.");
          }
          const cap = r.key.metadata?.maxRole;
          facts.apiKey = { id: r.key.id, userId, maxRole: typeof cap === "string" ? cap : null };
        }
      } catch {
        // Invalid keys are rejected by Better Auth itself with its own error.
      }
    }
    try {
      return await requestFacts.run(facts, () => inst.auth.handler(req));
    } catch (err) {
      console.error(JSON.stringify({ level: "error", msg: "auth request failed", project, path: url.pathname, err: String(err) }));
      return problem(500, "internal", "The auth engine hit an error. Details are in the auth engine log on the box (journalctl -u tiffin-auth).");
    }
  };
}
