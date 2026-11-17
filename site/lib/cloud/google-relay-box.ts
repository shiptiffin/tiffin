// Who may use the Google relay (google-relay.ts): a managed box whose
// subscription is active, with the licence of its current setup, calling
// from its own address. The same test as a check-in that counts.
import { clientIP } from "@shiptiffin/sdk/analytics";
import { heartbeatDecision } from "./actions";
import { boxById, tablesReady } from "./db";
import { relayApp, type RelayApp } from "./google-relay";
import { rateLimiter } from "../early-access-box";
import { licencePublicFrom, verifyLicence, type Licence } from "./licence";

// A box renews once an hour and connects rarely; this is far above both.
const limit = rateLimiter(120, 60 * 60_000);

const refuse = (status: number, error: string, error_description: string) =>
  Response.json({ error, error_description }, { status, headers: { "Cache-Control": "no-store" } });

export async function relayCaller(request: Request): Promise<{ app: RelayApp; licence: Licence } | Response> {
  const app = relayApp();
  const pub = licencePublicFrom(process.env.CLOUD_LICENCE_PUBLIC);
  if (!app || !pub || !(await tablesReady())) return refuse(503, "not_ready", "Connect with Google isn't set up at ShipTiffin yet.");
  const licence = verifyLicence(pub, request.headers.get("authorization")?.replace(/^Bearer\s+/i, "") ?? "");
  if (!licence) return refuse(401, "bad_licence", "This box's ShipTiffin licence did not check out.");
  if (!limit(`google-relay:${licence.box}`)) return refuse(429, "slow_down", "Too many requests from this box. Try again in an hour.");
  const d = heartbeatDecision(await boxById(licence.box), licence, clientIP(request));
  if (!d.counts) return refuse(403, "not_this_box", d.refused ?? "ShipTiffin doesn't manage this box.");
  if (!d.answer.active) return refuse(403, "not_active", "This box's ShipTiffin subscription is not active. Renew at shiptiffin.com/account.");
  return { app, licence };
}

/** A JSON body of at most 8 KB, or {}. */
export async function body(request: Request): Promise<Record<string, unknown>> {
  const text = await request.text();
  if (text.length > 8192) return {};
  try {
    const v = JSON.parse(text || "{}");
    return v && typeof v === "object" ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

export { refuse };
