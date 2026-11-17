// GET /api/box/google/callback: where Google sends the browser back after
// Connect with Google on a managed box (lib/cloud/google-relay.ts). It
// trades Google's code for the grant, and sends the browser on to the box's
// own callback with a short-lived code only that box can collect. Nothing
// is stored here, and no cookie or session is involved: the signed state
// says which box.
import { backToBox, googleToken, googleVerifier, idTokenEmail, openState, relayApp, sealHandoff } from "@/lib/cloud/google-relay";

export const dynamic = "force-dynamic";

const page = (words: string, status: number) =>
  new Response(`<!doctype html><meta charset="utf-8"><meta name="robots" content="noindex"><title>Google · ShipTiffin</title><p>${words}</p>`, {
    status,
    headers: { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer" },
  });

export async function GET(request: Request) {
  const app = relayApp();
  const q = new URL(request.url).searchParams;
  const state = q.get("state") ?? "";
  const st = app ? openState(app, state) : null;
  if (!app || !st) return page("This link is too old, or was not started from a box. Go back to your box and choose Connect with Google again.", 400);
  const back = (answer: { code: string } | { error: string }) => {
    const to = backToBox(st, answer);
    if (!to) return page("This box has no address to go back to.", 400);
    return new Response(null, { status: 303, headers: { Location: to, "Cache-Control": "no-store", "Referrer-Policy": "no-referrer" } });
  };
  const error = q.get("error");
  if (error) return back({ error: error === "access_denied" ? "access_denied" : "server_error" });
  const code = q.get("code") ?? "";
  if (!code || code.length > 2048) return back({ error: "server_error" });
  const { status, body } = await googleToken(app, { grant_type: "authorization_code", code, code_verifier: googleVerifier(app, state), redirect_uri: app.redirect });
  if (status !== 200 || !body.refresh_token) return back({ error: "server_error" });
  // The box checks the permissions too (and ends the access when one is missing): it gets what Google granted.
  return back({ code: sealHandoff(app, { b: st.b, c: st.c, r: body.refresh_token, sc: body.scope ?? "", em: idTokenEmail(body.id_token) }) });
}
