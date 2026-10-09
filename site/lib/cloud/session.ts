// Who is signed in, through the box's own sign-in (Better Auth engine,
// services.auth in tiffin.config.ts). Null when signed out, or when the
// engine is not there (a local build). Reads the request's headers, so pages
// that call it render per request.
//
// Every page and route handler that shows or changes an account's boxes,
// billing or the admin pages calls this itself (no layout or proxy does it
// for them), and every query it then makes is scoped to the account's id.
import "server-only";
import { sessionFor } from "@shiptiffin/sdk/auth";
import { headers } from "next/headers";
import { cache } from "react";

export type Account = { id: string; email: string; name: string; emailVerified: boolean };

// One look-up per page render (React's cache; in route handlers it is a plain call).
const lookUp = cache(async (fresh: boolean): Promise<Account | null> => {
  try {
    const { session } = await sessionFor(await headers(), fresh ? { fresh } : undefined);
    const u = session?.user;
    if (!u?.id || !u.email) return null;
    return { id: u.id, email: u.email.toLowerCase(), name: u.name, emailVerified: u.emailVerified === true };
  } catch {
    return null;
  }
});

/**
 * The signed-in account. `fresh` asks the sign-in engine itself rather than
 * trusting the signed session cookie, which can lag a sign-out by up to a
 * minute: for anything that changes a box or money, or shows billing or
 * admin data.
 */
export function currentAccount(opts?: { fresh?: boolean }): Promise<Account | null> {
  return lookUp(Boolean(opts?.fresh));
}
