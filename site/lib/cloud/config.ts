// Whether sign-up is open: every secret it needs is set and the provisioner
// has made its tables. Until then /start shows the sign-up list instead of
// erroring. Names only here; values stay in the project's secrets. The
// website holds public keys only: it seals Hetzner tokens it can't open, and
// checks licences it can't sign (the private halves are the provisioner project's).
import type { KeyObject } from "node:crypto";
import { tablesReady } from "./db";
import { licencePublicFrom } from "./licence";
import { sealPublicFrom } from "./seal";
import type { Account } from "./session";

export function missingSecrets(env: Record<string, string | undefined> = process.env): string[] {
  const missing: string[] = [];
  if (!env.DATABASE_URL) missing.push("DATABASE_URL");
  if (!env.STRIPE_SECRET_KEY?.trim()) missing.push("STRIPE_SECRET_KEY");
  else if (env.STRIPE_SECRET_KEY.trim().startsWith("sk_live_") && env.STRIPE_LIVE !== "1") missing.push("STRIPE_SECRET_KEY (a live key; test mode only for now)");
  if (!env.STRIPE_WEBHOOK_SECRET?.trim()) missing.push("STRIPE_WEBHOOK_SECRET");
  if (!sealPublicFrom(env.CLOUD_SEAL_PUBLIC)) missing.push("CLOUD_SEAL_PUBLIC");
  if (!licencePublicFrom(env.CLOUD_LICENCE_PUBLIC)) missing.push("CLOUD_LICENCE_PUBLIC");
  return missing;
}

export async function signupOpen(): Promise<boolean> {
  if (missingSecrets().length > 0) return false;
  return tablesReady();
}

/** The worker's public key: what the website seals Hetzner tokens to. */
export function sealPublic(): KeyObject {
  const k = sealPublicFrom(process.env.CLOUD_SEAL_PUBLIC);
  if (!k) throw new Error("CLOUD_SEAL_PUBLIC is not set");
  return k;
}

/** The founding coupon's id (Stripe), when the offer is configured. */
export const foundingCoupon = () => process.env.STRIPE_COUPON_FOUNDING?.trim() || null;

/**
 * Owner-only pages: accounts whose id is in CLOUD_ADMIN_USER_IDS (comma
 * separated), and only with a verified email. Ids never change hands the way
 * an email address can (a lapsed domain, a recycled mailbox, a Google account
 * on an address its holder no longer controls).
 */
export function isAdmin(acct: Pick<Account, "id" | "emailVerified"> | null | undefined, env: Record<string, string | undefined> = process.env): boolean {
  if (!acct?.id || !acct.emailVerified) return false;
  const ids = (env.CLOUD_ADMIN_USER_IDS ?? "").split(",").map((s) => s.trim()).filter(Boolean);
  return ids.includes(acct.id);
}
