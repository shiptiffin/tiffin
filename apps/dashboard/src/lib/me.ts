import { useQuery } from "@tanstack/react-query";
import type { Principal } from "@/api/client";
import { q } from "@/api/queries";

const ladder = ["read", "plan", "apply:reversible", "apply:outbound", "apply:irreversible"];

/** Whether a principal holds a scope, directly, via "*", or via the apply ladder. */
export function holds(p: Pick<Principal, "scopes"> | undefined, scope: string) {
  const s = p?.scopes ?? [];
  if (s.includes("*") || s.includes(scope)) return true;
  const i = ladder.indexOf(scope);
  return i >= 0 && s.some((x) => ladder.indexOf(x) > i);
}

/** Who is signed in, and what the interface should offer them. */
export function useMe() {
  const { data: me } = useQuery(q.whoami);
  const role = me?.role ?? (me?.kind === "owner" ? "owner" : undefined);
  return {
    me,
    role,
    name: me?.personName ?? me?.name,
    admin: role === "owner" || role === "admin" || holds(me, "*"),
    can: (scope: string) => holds(me, scope),
  };
}

export const roleCopy: Record<string, { label: string; blurb: string }> = {
  owner: { label: "Owner", blurb: "Everything, including the box itself. A box has exactly one owner." },
  admin: { label: "Admin", blurb: "Everything except changing the owner: people, tokens, any change." },
  member: {
    label: "Member",
    blurb: "Plans and applies changes that can be undone or that reach outside the box. Can't destroy data or manage people.",
  },
  viewer: { label: "Viewer", blurb: "Sees everything, changes nothing." },
};

/** Names for token IDs (deploys and changes record who by token). */
export function useWho() {
  const { data } = useQuery({ ...q.tokenNames, retry: false });
  return (id?: string) => (id ? (data?.get(id)?.who ?? (id.startsWith("tok_") ? "a token" : id)) : "someone");
}
