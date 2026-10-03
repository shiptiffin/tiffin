// Organization roles. Four built-in roles, strictly ordered:
// viewer < member < admin < owner. Nobody can grant, invite with or change
// someone to a role above their own, and an API key acts with at most the
// role of the person who made it (lower if the key carries a cap).
import { createAccessControl } from "better-auth/plugins/access";
import { defaultStatements } from "better-auth/plugins/organization/access";

export const ROLES = ["viewer", "member", "admin", "owner"] as const;
export type Role = (typeof ROLES)[number];

const RANK: Record<Role, number> = { viewer: 0, member: 1, admin: 2, owner: 3 };

export function isRole(r: unknown): r is Role {
  return typeof r === "string" && (ROLES as readonly string[]).includes(r);
}

/**
 * rank of a stored role string. Better Auth may store several roles as
 * "admin,member"; the strongest one counts. Unknown roles rank -1.
 */
export function rank(role: string | null | undefined): number {
  if (!role) return -1;
  let best = -1;
  for (const r of role.split(",")) {
    const t = r.trim();
    if (isRole(t)) best = Math.max(best, RANK[t]);
  }
  return best;
}

/** The weaker of two roles (for capping an API key by its sponsor). */
export function weaker(a: string | null | undefined, b: string | null | undefined): Role | null {
  const ra = rank(a);
  const rb = rank(b);
  const r = Math.min(ra, rb);
  if (r < 0) return null;
  return ROLES[r] ?? null;
}

export const statements = {
  ...defaultStatements,
  apiKey: ["create", "read", "update", "delete"],
} as const;

export const ac = createAccessControl(statements);

export const roles = {
  owner: ac.newRole({
    organization: ["update", "delete"],
    member: ["create", "update", "delete"],
    invitation: ["create", "cancel"],
    team: ["create", "update", "delete"],
    ac: ["create", "read", "update", "delete"],
    apiKey: ["create", "read", "update", "delete"],
  }),
  admin: ac.newRole({
    organization: ["update"],
    member: ["create", "update", "delete"],
    invitation: ["create", "cancel"],
    team: ["create", "update", "delete"],
    ac: ["read"],
    apiKey: ["create", "read", "update", "delete"],
  }),
  member: ac.newRole({
    organization: [],
    member: [],
    invitation: [],
    team: [],
    ac: ["read"],
    apiKey: ["read"],
  }),
  viewer: ac.newRole({
    organization: [],
    member: [],
    invitation: [],
    team: [],
    ac: [],
    apiKey: [],
  }),
};
