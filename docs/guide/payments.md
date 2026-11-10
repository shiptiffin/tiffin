# Taking payments with Stripe

Tiffin has no payments module. Stripe Checkout takes the payment, a webhook records it
in your Postgres, and your app reads that table. Nothing here needs more than a
route handler, a SQL file and three secrets. The examples use Web `Request` and
`Response`, so they fit Next route handlers, Astro endpoints and Hono alike.

```
buyer -> POST /api/checkout -> Stripe Checkout -> buyer pays
Stripe -> POST /api/stripe/webhook -> row in `access` -> app checks `access`
```

## Secrets

Make a restricted key in Stripe (Developers › API keys › Create restricted key) with
only these permissions: Checkout Sessions write, Customers write, Products read,
Prices read, PaymentIntents read, Charges read. The webhook secret (`whsec_...`)
appears when you add the endpoint (see [Webhook](#webhook)). Make the prices in Stripe
and note their ids.

```bash
tiffin secrets set shop STRIPE_SECRET_KEY --value "$(pbpaste)"      # rk_live_... on the clipboard
tiffin secrets set shop STRIPE_WEBHOOK_SECRET --value "$(pbpaste)"
tiffin secrets set shop STRIPE_PRICE_PRO --value price_1Abc...      # ids are not secret
```

Copy the value to your clipboard in the Stripe dashboard and run the command. The
value never appears in a transcript or in shell history, and an agent can run the same
line without seeing the key. For a second app on the same Stripe account, move the
values inside the box:

```bash
tiffin secrets copy blog --from shop --names STRIPE_SECRET_KEY,STRIPE_WEBHOOK_SECRET
```

Each endpoint has its own `whsec_`, so a second app that gets its own webhook endpoint
sets its own `STRIPE_WEBHOOK_SECRET` rather than copying this one. Use `rk_test_`
keys and test prices until you go live. See [secrets in agents](agents.md#starting-a-new-project-from-what-you-have).

```ts
// lib/stripe.ts
import Stripe from "stripe";
export const stripe = new Stripe(process.env.STRIPE_SECRET_KEY!, { apiVersion: "<the version your stripe package pins>", maxNetworkRetries: 2 });
```

Pin the API version so a Stripe-side default change never alters what the webhook receives.

## Checkout

Send the buyer to a Checkout Session. No account is needed first: the email they type
at Checkout is their access, and they sign in with it afterwards.

```ts
// POST /api/checkout
export async function POST(request: Request) {
  const { plan } = await request.json();                      // validate against your own list
  const price = { once: process.env.STRIPE_PRICE_ONCE, pro: process.env.STRIPE_PRICE_PRO }[plan as string];
  if (!price) return Response.json({ error: "unknown_plan" }, { status: 400 });

  const origin = new URL(process.env.TIFFIN_URL!).origin;     // this deploy's own address
  const session = await stripe.checkout.sessions.create({
    mode: plan === "pro" ? "subscription" : "payment",        // one-time or recurring
    line_items: [{ price, quantity: 1 }],
    allow_promotion_codes: true,
    metadata: { app: "shop", plan },                          // read again in the webhook
    ...(plan === "pro" ? { subscription_data: { metadata: { app: "shop", plan } } } : { customer_creation: "always" }),
    success_url: `${origin}/welcome?session_id={CHECKOUT_SESSION_ID}`,
    cancel_url: `${origin}/pricing`,
  });
  return Response.json({ url: session.url });                 // the browser goes to session.url
}
```

`TIFFIN_URL` is the public URL of the deploy that handles the request, production or
preview, so the buyer comes back to the host they started on. A plain HTML form can post
to the route too; answer `303` with `Location: session.url`.

If the buyer is already signed in with a verified email, pass `customer_email` so
Checkout fills it in. Never trust the success page as proof of payment: it only says
the buyer came back. The webhook grants access.

## Webhook

Add the endpoint in Stripe (Developers › Webhooks) at `https://<your host>/api/stripe/webhook`
and subscribe it to the events below. Stripe shows the `whsec_` once it exists.

- Keep the route **outside `/api/auth/`**. The box reserves that path and requests under it never reach your app.
- If your site uses trailing slashes (Astro `trailingSlash: "always"`, `trailingSlash: true` in Next), register the URL **with** the slash. A redirect to add it turns Stripe's POST into a GET and the event is lost.
- Read the **raw body** with `request.text()` before anything parses it. The signature covers those exact bytes.

```ts
// POST /api/stripe/webhook
export async function POST(request: Request) {
  const signature = request.headers.get("stripe-signature");
  if (!signature) return Response.json({ error: "no_signature" }, { status: 400 });
  let event: Stripe.Event;
  try {
    event = await stripe.webhooks.constructEventAsync(await request.text(), signature, process.env.STRIPE_WEBHOOK_SECRET!);
  } catch {
    return Response.json({ error: "bad_signature" }, { status: 400 });
  }

  try {
    switch (event.type) {
      case "checkout.session.completed":
      case "checkout.session.async_payment_succeeded": {
        const s = event.data.object;
        if (s.metadata?.app !== "shop") break;                // the Stripe account may serve other products
        if (s.payment_status !== "paid" && s.payment_status !== "no_payment_required") break; // bank debits finish later
        const email = (s.customer_details?.email ?? s.customer_email ?? "").trim().toLowerCase();
        if (!email) break;
        const pi = typeof s.payment_intent === "string" ? s.payment_intent : s.payment_intent?.id ?? null;
        await sql`
          insert into access (email, plan, stripe_session_id, stripe_payment_intent, amount, currency)
          values (${email}, ${s.metadata.plan}, ${s.id}, ${pi}, ${s.amount_total ?? 0}, ${s.currency ?? "usd"})
          on conflict (stripe_session_id) do nothing`;         // Stripe retries and re-sends: safe to run twice
        break;
      }
      case "charge.refunded": {
        const c = event.data.object;
        const pi = typeof c.payment_intent === "string" ? c.payment_intent : c.payment_intent?.id;
        if (pi && c.refunded) await sql`update access set refunded_at = now() where stripe_payment_intent = ${pi} and refunded_at is null`;
        break;                                                // a partial refund (c.refunded false) keeps access
      }
    }
  } catch (e) {
    return Response.json({ error: "handler_failed" }, { status: 500 }); // Stripe retries a 500 later
  }
  return Response.json({ received: true });
}
```

Answer `2xx` for events you ignore, `400` for a bad signature and `500` only when
your own work failed. Do not do slow work in the handler.

**Recurring plans** need more events. Add `customer.subscription.updated`,
`customer.subscription.deleted`, `invoice.paid` and `invoice.payment_failed`, and store the
subscription id and its `current_period_end`. Access then lasts until the period end
you last saw, and each `invoice.paid` moves it forward.

## The access table

A plain SQL migration, run by the app's `release` command, which gets
`DIRECT_DATABASE_URL` (see [Migrations and preview databases](apps.md#migrations-and-preview-databases)).

```sql
-- db/migrations/001_access.sql
create table if not exists access (
  id bigserial primary key,
  email text not null check (email = lower(email)),
  plan text not null,
  stripe_session_id text not null unique,        -- the idempotency key
  stripe_payment_intent text,
  amount integer not null default 0,             -- smallest currency unit
  currency text not null default 'usd',
  expires_at timestamptz,                        -- null: never expires
  refunded_at timestamptz,
  created_at timestamptz not null default now()
);
create index if not exists access_email_idx on access (email);
create index if not exists access_payment_intent_idx on access (stripe_payment_intent);
```

```ts
apps: { web: { framework: "next", release: "node scripts/migrate.mjs" } }
```

`scripts/migrate.mjs` reads `db/migrations/*.sql` in name order and applies each file
once in a transaction, recording it in a table of your own (`schema_migrations`). Use
any migration tool you like instead; the table is the part that matters.

Connect from the app through the pooled `DATABASE_URL` with postgres.js and
`prepare: false`, as in [Connecting from your app](data.md#connecting-from-your-app).
Never create objects in the `tiffin_auth` schema or with a `tiffin` prefix: they belong to the box.

## Linking a payment to a signed-in user

The webhook does not need a user. It records the buyer's verified, lowercased email.
At request time, the rule is: the signed-in user's **verified** email has a row that is
not refunded and not expired.

```ts
import { getSession } from "@shiptiffin/sdk/auth";

export async function hasAccess(request: Request) {
  const s = await getSession(request);
  if (!s?.user?.emailVerified) return false;                   // an unverified address proves nothing
  const rows = await sql`
    select 1 from access
    where email = ${String(s.user.email).toLowerCase()} and refunded_at is null
      and (expires_at is null or expires_at > now()) limit 1`;
  return rows.length > 0;
}
```

The buyer pays, then signs in (or signs up) with the same address, and the row is
found. Checking the verified flag matters: otherwise anyone could sign up with someone
else's address and claim their purchase.

If you would rather create the account when the payment arrives, so the buyer gets a
sign-in email straight away, `@shiptiffin/sdk/auth` has `getUserByEmail(email)` and
`createUser({ email, name?, emailVerified? })` for calling from the webhook. This is
new; see [sign-in](auth.md) for how accounts work before relying on it. Matching by
email at request time needs neither and is the simpler default.

## Organizations and seats

For per-seat plans, make the subscription belong to an organization (see
[sign-in](auth.md), `organizations: true`). Store the organization id in the Checkout
metadata, set `quantity` to the member count, and when members join or leave call
`stripe.subscriptions.update` with the new quantity. Check access by the user's
active organization, not by email.

## Testing

- **Previews use the project's secrets**, so a preview with a live key takes real
  money. Each preview gets its own copy of the database unless the project sets
  `previews: "shared"`; the secrets are shared either way. Use test keys while building.
- **Sign-in mail from a preview** reaches real users like production's ([previews](auth.md#previews)). Sign in on a preview with an address you own.
- **Test a live price** with a 100% promotion code (a coupon that makes the total zero). Checkout then reports `payment_status: "no_payment_required"` and the webhook grants access, which the handler above allows for.
- **Webhook on a preview:** add a second endpoint for the preview's host, or point the one endpoint at the new URL at launch. Editing an endpoint's URL keeps its signing secret, so nothing needs to be re-set. A second endpoint has its own secret.

## What the box does not do

- No tax. Turn on Stripe Tax and set `automatic_tax: { enabled: true }` on the session if you must collect it.
- No invoices, receipts or dunning beyond what Stripe sends. Configure them in Stripe.
- No refund button. Refund in the Stripe dashboard; the `charge.refunded` event reaches your webhook and closes access.
- No customer portal. Create a Stripe billing portal session yourself if buyers need to manage a subscription.
- No stored card data. It never touches the box.
