// Whether sign-up is open: every secret it needs is set and the cloud worker
// has made its tables. Until then /start shows the sign-up list instead of
// erroring. Names only here; values stay in the project's secrets.
import { tablesReady } from "./db";
import { publicKeyFromSeed } from "./licence";
import { kekFrom } from "./seal";

export function missingSecrets(env: Record<string, string | undefined> = process.env): string[] {
  const missing: string[] = [];
  if (!env.DATABASE_URL) missing.push("DATABASE_URL");
  if (!env.STRIPE_SECRET_KEY?.trim()) missing.push("STRIPE_SECRET_KEY");
  else if (env.STRIPE_SECRET_KEY.trim().startsWith("sk_live_") && env.STRIPE_LIVE !== "1") missing.push("STRIPE_SECRET_KEY (a live key; test mode only for now)");
  if (!env.STRIPE_WEBHOOK_SECRET?.trim()) missing.push("STRIPE_WEBHOOK_SECRET");
  if (!kekFrom(env.CLOUD_KEK)) missing.push("CLOUD_KEK");
  if (!publicKeyFromSeed(env.CLOUD_LICENCE_KEY)) missing.push("CLOUD_LICENCE_KEY");
  return missing;
}

export async function signupOpen(): Promise<boolean> {
  if (missingSecrets().length > 0) return false;
  return tablesReady();
}

export function kek(): Buffer {
  const k = kekFrom(process.env.CLOUD_KEK);
  if (!k) throw new Error("CLOUD_KEK is not set");
  return k;
}

/** The founding coupon's id (Stripe), when the offer is configured. */
export const foundingCoupon = () => process.env.STRIPE_COUPON_FOUNDING?.trim() || null;

/** Owner-only pages: addresses in CLOUD_ADMIN_EMAILS (comma separated). */
export function isAdmin(email: string | null | undefined): boolean {
  if (!email) return false;
  const list = (process.env.CLOUD_ADMIN_EMAILS ?? "").split(",").map((s) => s.trim().toLowerCase()).filter(Boolean);
  return list.includes(email.toLowerCase());
}
