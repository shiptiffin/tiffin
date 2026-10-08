// Who is signed in, through the box's own sign-in (Better Auth engine,
// services.auth in tiffin.config.ts). Null when signed out, or when the
// engine is not there (a local build). Reads the request's headers, so pages
// that call it render per request.
import { sessionFor } from "@shiptiffin/sdk/auth";
import { headers } from "next/headers";

export type Account = { id: string; email: string; name: string };

export async function currentAccount(opts?: { fresh?: boolean }): Promise<Account | null> {
  try {
    const { session } = await sessionFor(await headers(), opts);
    const u = session?.user;
    if (!u?.id || !u.email) return null;
    return { id: u.id, email: u.email.toLowerCase(), name: u.name };
  } catch {
    return null;
  }
}
