// POST /api/stripe/webhook: Stripe's events for box subscriptions. The
// signature is checked over the raw body; each event is handled once. The
// endpoint in Stripe: https://shiptiffin.com/api/stripe/webhook with
// checkout.session.completed, customer.subscription.updated,
// customer.subscription.deleted, invoice.paid and invoice.payment_failed.
import { sendNotices } from "@/lib/cloud/actions";
import { handleEvent, type StripeEvent } from "@/lib/cloud/billing";
import { pgBilling, tablesReady } from "@/lib/cloud/db";
import { verifyStripeSignature } from "@/lib/cloud/stripe";

export const dynamic = "force-dynamic";

export async function POST(request: Request) {
  const secret = process.env.STRIPE_WEBHOOK_SECRET?.trim();
  if (!secret) return new Response("webhooks are not set up", { status: 503 });
  const raw = await request.text();
  if (raw.length > 512_000 || !verifyStripeSignature(raw, request.headers.get("stripe-signature"), secret)) {
    return new Response("bad signature", { status: 400 });
  }
  let ev: StripeEvent;
  try {
    ev = JSON.parse(raw);
  } catch {
    return new Response("bad body", { status: 400 });
  }
  if (!(await tablesReady())) return new Response("not ready", { status: 503 }); // Stripe retries
  try {
    const { notices } = await handleEvent(pgBilling(), ev);
    await sendNotices(notices);
    return Response.json({ received: true });
  } catch (e) {
    console.error("stripe webhook", ev.type, ev.id, e instanceof Error ? e.message : e);
    return new Response("failed; retry", { status: 500 });
  }
}
