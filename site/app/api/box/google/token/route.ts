// POST /api/box/google/token: a managed box collects its grant, or renews
// its access (lib/cloud/google-relay.ts). Authorization: Bearer <licence>.
//
//   {grant: "code", code, verifier}        -> {refresh_token, scope, email}
//   {grant: "refresh", refresh_token}      -> {access_token, expires_in}
//
// Renewing needs the Google app's secret, which only this site holds; the
// box keeps the refresh token. Google's own refusals come back as they are
// ({error, error_description}, 400), so the box can say why.
import { googleToken, openHandoff } from "@/lib/cloud/google-relay";
import { body, refuse, relayCaller } from "@/lib/cloud/google-relay-box";

export const dynamic = "force-dynamic";

const noStore = { headers: { "Cache-Control": "no-store" } };

export async function POST(request: Request) {
  const who = await relayCaller(request);
  if (who instanceof Response) return who;
  const b = await body(request);
  if (b.grant === "code") {
    const h = typeof b.code === "string" && typeof b.verifier === "string" ? openHandoff(who.app, b.code, who.licence.box, b.verifier) : null;
    if (!h) return refuse(400, "invalid_grant", "That code is not this box's, or is more than two minutes old. Choose Connect with Google again.");
    return Response.json({ refresh_token: h.r, scope: h.sc, email: h.em }, noStore);
  }
  if (b.grant === "refresh") {
    if (typeof b.refresh_token !== "string" || !b.refresh_token || b.refresh_token.length > 2048) return refuse(400, "invalid_request", "refresh_token is required.");
    const { status, body: g } = await googleToken(who.app, { grant_type: "refresh_token", refresh_token: b.refresh_token });
    if (status !== 200 || !g.access_token) return refuse(status >= 500 ? 502 : 400, g.error ?? "server_error", g.error_description ?? "Google did not renew the access.");
    return Response.json({ access_token: g.access_token, expires_in: g.expires_in ?? 3600 }, noStore);
  }
  return refuse(400, "unsupported_grant_type", "grant is code or refresh.");
}
