// Link invites: a shareable link that lets anyone who signs in join an
// organization with a fixed role, up to a number of uses, until it expires.
// Better Auth's organization plugin only has email invites; this adds the
// other half. Only the token's hash is stored.
import type { BetterAuthPlugin } from "better-auth";
import { APIError, createAuthEndpoint, sessionMiddleware } from "better-auth/api";
import { z } from "zod";
import { currentFacts } from "./context";
import { ROLES, rank, roles, weaker } from "./roles";

export type InviteLinkOptions = {
  /** Runs `UPDATE ... uses = uses + 1 WHERE uses < max` atomically; returns rows changed. */
  claimUse: (id: string) => Promise<number>;
  /** Builds the link people open, from the request origin. */
  linkURL: (token: string, request?: Request) => string;
};

const TOKEN_BYTES = 24;

function newToken(): string {
  const b = crypto.getRandomValues(new Uint8Array(TOKEN_BYTES));
  return "tfi_" + Buffer.from(b).toString("base64url");
}

export function hashToken(token: string): string {
  return new Bun.CryptoHasher("sha256").update(token).digest("hex");
}

type Adapter = {
  findOne: <T>(q: { model: string; where: { field: string; value: unknown }[] }) => Promise<T | null>;
  findMany: <T>(q: { model: string; where?: { field: string; value: unknown }[]; sortBy?: { field: string; direction: "asc" | "desc" }; limit?: number }) => Promise<T[]>;
  create: <T>(q: { model: string; data: Record<string, unknown>; forceAllowId?: boolean }) => Promise<T>;
  update: <T>(q: { model: string; where: { field: string; value: unknown }[]; update: Record<string, unknown> }) => Promise<T | null>;
};

type Member = { id: string; userId: string; organizationId: string; role: string };
type Link = {
  id: string;
  organizationId: string;
  role: string;
  tokenHash: string;
  maxUses: number;
  uses: number;
  expiresAt: Date;
  createdBy: string;
  createdAt: Date;
  revokedAt: Date | null;
};
type Org = { id: string; name: string; slug: string; logo?: string | null };

/** The caller's role in an org, lowered to an API key's cap when one is in use. */
export async function effectiveRole(adapter: Adapter, userId: string, organizationId: string): Promise<string | null> {
  const m = await adapter.findOne<Member>({
    model: "member",
    where: [
      { field: "userId", value: userId },
      { field: "organizationId", value: organizationId },
    ],
  });
  if (!m) return null;
  const cap = currentFacts().apiKey?.maxRole;
  return cap ? weaker(m.role, cap) : weaker(m.role, "owner");
}

function can(role: string | null, perm: Record<string, string[]>): boolean {
  if (!role) return false;
  const r = roles[role as keyof typeof roles];
  return !!r && r.authorize(perm as never).success;
}

function linkView(l: Link) {
  return {
    id: l.id,
    organizationId: l.organizationId,
    role: l.role,
    maxUses: l.maxUses,
    uses: l.uses,
    expiresAt: l.expiresAt,
    createdBy: l.createdBy,
    createdAt: l.createdAt,
    revokedAt: l.revokedAt,
    active: !l.revokedAt && new Date(l.expiresAt) > new Date() && l.uses < l.maxUses,
  };
}

function why(l: Link | null): string | null {
  if (!l) return "This invite link doesn't exist. Ask for a new one.";
  if (l.revokedAt) return "This invite link was turned off. Ask for a new one.";
  if (new Date(l.expiresAt) <= new Date()) return "This invite link has expired. Ask for a new one.";
  if (l.uses >= l.maxUses) return "This invite link has been used as many times as it allows. Ask for a new one.";
  return null;
}

export const inviteLinks = (o: InviteLinkOptions) =>
  ({
    id: "tiffin-invite-links",
    schema: {
      orgInviteLink: {
        fields: {
          organizationId: { type: "string", required: true, references: { model: "organization", field: "id", onDelete: "cascade" }, index: true },
          role: { type: "string", required: true },
          tokenHash: { type: "string", required: true, unique: true },
          maxUses: { type: "number", required: true },
          uses: { type: "number", required: true, defaultValue: 0 },
          expiresAt: { type: "date", required: true },
          createdBy: { type: "string", required: true },
          createdAt: { type: "date", required: true },
          revokedAt: { type: "date", required: false },
        },
      },
    },
    endpoints: {
      createInviteLink: createAuthEndpoint(
        "/invite-link/create",
        {
          method: "POST",
          use: [sessionMiddleware],
          body: z.object({
            organizationId: z.string().optional(),
            role: z.enum(ROLES).default("member"),
            maxUses: z.number().int().min(1).max(1000).default(25),
            expiresInDays: z.number().int().min(1).max(30).default(7),
          }),
        },
        async (ctx) => {
          const session = ctx.context.session;
          const orgId = ctx.body.organizationId ?? (session.session as { activeOrganizationId?: string }).activeOrganizationId;
          if (!orgId) throw new APIError("BAD_REQUEST", { code: "NO_ORGANIZATION", message: "Pass organizationId, or set an active organization first." });
          const adapter = ctx.context.adapter as unknown as Adapter;
          const mine = await effectiveRole(adapter, session.user.id, orgId);
          if (!can(mine, { invitation: ["create"] })) {
            throw new APIError("FORBIDDEN", { code: "NOT_ALLOWED", message: "Only owners and admins can create invite links." });
          }
          if (rank(ctx.body.role) > rank(mine)) {
            throw new APIError("FORBIDDEN", { code: "ROLE_ABOVE_YOURS", message: `You can't invite people as ${ctx.body.role}: that's above your own role (${mine}).` });
          }
          const token = newToken();
          const now = new Date();
          const link = await adapter.create<Link>({
            model: "orgInviteLink",
            data: {
              organizationId: orgId,
              role: ctx.body.role,
              tokenHash: hashToken(token),
              maxUses: ctx.body.maxUses,
              uses: 0,
              expiresAt: new Date(now.getTime() + ctx.body.expiresInDays * 86_400_000),
              createdBy: session.user.id,
              createdAt: now,
            },
          });
          return ctx.json({ ...linkView(link), token, url: o.linkURL(token, ctx.request) });
        },
      ),
      listInviteLinks: createAuthEndpoint(
        "/invite-link/list",
        { method: "GET", use: [sessionMiddleware], query: z.object({ organizationId: z.string().optional() }).optional() },
        async (ctx) => {
          const session = ctx.context.session;
          const orgId = ctx.query?.organizationId ?? (session.session as { activeOrganizationId?: string }).activeOrganizationId;
          if (!orgId) throw new APIError("BAD_REQUEST", { code: "NO_ORGANIZATION", message: "Pass organizationId, or set an active organization first." });
          const adapter = ctx.context.adapter as unknown as Adapter;
          const mine = await effectiveRole(adapter, session.user.id, orgId);
          if (!can(mine, { invitation: ["create"] })) throw new APIError("FORBIDDEN", { code: "NOT_ALLOWED", message: "Only owners and admins can see invite links." });
          const links = await adapter.findMany<Link>({ model: "orgInviteLink", where: [{ field: "organizationId", value: orgId }], sortBy: { field: "createdAt", direction: "desc" }, limit: 100 });
          return ctx.json(links.map(linkView));
        },
      ),
      revokeInviteLink: createAuthEndpoint(
        "/invite-link/revoke",
        { method: "POST", use: [sessionMiddleware], body: z.object({ id: z.string() }) },
        async (ctx) => {
          const adapter = ctx.context.adapter as unknown as Adapter;
          const link = await adapter.findOne<Link>({ model: "orgInviteLink", where: [{ field: "id", value: ctx.body.id }] });
          if (!link) throw new APIError("NOT_FOUND", { code: "NOT_FOUND", message: "No invite link with that id." });
          const mine = await effectiveRole(adapter, ctx.context.session.user.id, link.organizationId);
          if (!can(mine, { invitation: ["cancel"] })) throw new APIError("FORBIDDEN", { code: "NOT_ALLOWED", message: "Only owners and admins can turn off invite links." });
          const updated = await adapter.update<Link>({ model: "orgInviteLink", where: [{ field: "id", value: link.id }], update: { revokedAt: new Date() } });
          return ctx.json(linkView(updated ?? link));
        },
      ),
      getInviteLink: createAuthEndpoint(
        "/invite-link/get",
        { method: "GET", query: z.object({ token: z.string().min(8).max(200) }) },
        async (ctx) => {
          const adapter = ctx.context.adapter as unknown as Adapter;
          const link = await adapter.findOne<Link>({ model: "orgInviteLink", where: [{ field: "tokenHash", value: hashToken(ctx.query.token) }] });
          const reason = why(link);
          if (!link) return ctx.json({ valid: false, reason, organization: null, role: null });
          const org = await adapter.findOne<Org>({ model: "organization", where: [{ field: "id", value: link.organizationId }] });
          return ctx.json({
            valid: !reason,
            reason,
            role: link.role,
            expiresAt: link.expiresAt,
            organization: org ? { id: org.id, name: org.name, slug: org.slug, logo: org.logo ?? null } : null,
          });
        },
      ),
      acceptInviteLink: createAuthEndpoint(
        "/invite-link/accept",
        { method: "POST", use: [sessionMiddleware], body: z.object({ token: z.string().min(8).max(200) }) },
        async (ctx) => {
          const adapter = ctx.context.adapter as unknown as Adapter;
          const user = ctx.context.session.user;
          const link = await adapter.findOne<Link>({ model: "orgInviteLink", where: [{ field: "tokenHash", value: hashToken(ctx.body.token) }] });
          const reason = why(link);
          if (!link || reason) throw new APIError("BAD_REQUEST", { code: "INVITE_LINK_INVALID", message: reason ?? "Invalid invite link." });
          const existing = await adapter.findOne<Member>({
            model: "member",
            where: [
              { field: "userId", value: user.id },
              { field: "organizationId", value: link.organizationId },
            ],
          });
          if (existing) return ctx.json({ member: existing, alreadyMember: true });
          if ((await o.claimUse(link.id)) !== 1) {
            throw new APIError("BAD_REQUEST", { code: "INVITE_LINK_INVALID", message: why({ ...link, uses: link.maxUses }) ?? "Invalid invite link." });
          }
          const member = await adapter.create<Member>({
            model: "member",
            data: { organizationId: link.organizationId, userId: user.id, role: link.role, createdAt: new Date() },
          });
          return ctx.json({ member, alreadyMember: false });
        },
      ),
    },
  }) satisfies BetterAuthPlugin;
