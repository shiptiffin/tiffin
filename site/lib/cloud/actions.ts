// What the control plane's pages and routes do, server side. Every function
// checks the box belongs to the account before touching it.
import { handleEvent, EXTRAS_ON, type Notice } from "./billing";
import { foundingCoupon, kek } from "./config";
import * as q from "./db";
import { deliver, mails, SITE } from "./emails";
import { checkToken, EU_LOCATIONS, EU_TYPES, family, RESIZE_TYPES, US_LOCATIONS, US_TYPES } from "./hetzner";
import { boxDomain, dashboardUrl, nameProblem } from "./names";
import { fingerprint, open, ownerAAD, seal, tokenAAD } from "./seal";
import type { Account } from "./session";
import { FOUNDING_LIMIT, StripeError, stripeClient } from "./stripe";
import { decide, probe, type MonitorBox } from "./monitor";

export class ActionError extends Error {
  constructor(
    message: string,
    readonly status = 400,
  ) {
    super(message);
  }
}

export const stripe = () => stripeClient(process.env.STRIPE_SECRET_KEY!.trim());

/** Sends the emails billing events asked for. */
export async function sendNotices(notices: Notice[]) {
  for (const n of notices) {
    const b = { email: n.box.email, name: n.box.name };
    if (n.kind === "paid") await deliver(mails.paid(b));
    if (n.kind === "extras_paused") await deliver(mails.extras_paused(b, n.until ?? new Date(Date.now() + 30 * 86_400_000)));
    if (n.kind === "extras_resumed") await deliver(mails.extras_resumed(b));
    if (n.kind === "payment_failed") await deliver(mails.payment_failed(b));
  }
}

// ---- pay ----

/** A Checkout session for a new box: $19 a month, the founding coupon while it lasts. */
export async function startCheckout(acct: Account, base = SITE): Promise<string> {
  const box = await q.pendingBox(acct.id, acct.email);
  const s = stripe();
  const price = await s.monthlyPrice();
  const coupon = foundingCoupon();
  const founding = coupon != null && (await q.foundingCount()) < FOUNDING_LIMIT && (await s.couponOpen(coupon));
  const customer = await q.customerFor(acct.id);
  const params: Record<string, unknown> = {
    mode: "subscription",
    line_items: [{ price, quantity: 1 }],
    success_url: `${base}/start?box=${box.id}&session_id={CHECKOUT_SESSION_ID}`,
    cancel_url: `${base}/start?canceled=1`,
    client_reference_id: box.id,
    metadata: { box_id: box.id },
    subscription_data: { metadata: { box_id: box.id } },
    ...(customer ? { customer } : { customer_email: acct.email }),
  };
  const minute = Math.floor(Date.now() / 60_000);
  try {
    const cs = await s.createCheckout(founding ? { ...params, discounts: [{ coupon }] } : params, `checkout:${box.id}:${founding}:${minute}`);
    return cs.url;
  } catch (e) {
    // The coupon ran out between the check and the session: full price.
    if (founding && e instanceof StripeError && (e.param?.startsWith("discounts") || /coupon/i.test(e.message))) {
      return (await s.createCheckout(params, `checkout:${box.id}:false:${minute}`)).url;
    }
    throw e;
  }
}

/** Back from Checkout: confirm with Stripe at once rather than wait for the webhook. */
export async function confirmCheckout(acct: Account, boxId: string, sessionId: string): Promise<void> {
  const box = await q.boxFor(boxId, acct.id);
  if (!box || box.status !== "awaiting_payment" || !/^cs_[A-Za-z0-9_]+$/.test(sessionId)) return;
  try {
    const cs = await stripe().getCheckout(sessionId);
    if ((cs.metadata?.box_id ?? cs.client_reference_id) !== box.id || cs.status !== "complete") return;
    const r = await handleEvent(q.pgBilling(), { id: `return:${cs.id}`, type: "checkout.session.completed", created: cs.created ?? Math.floor(Date.now() / 1000), data: { object: cs } });
    await sendNotices(r.notices);
  } catch (e) {
    console.error("confirm checkout", e instanceof Error ? e.message : e);
  }
}

export async function billingPortal(acct: Account, base = SITE): Promise<string> {
  const customer = await q.portalCustomer(acct.id);
  if (!customer) throw new ActionError("There's no billing account yet: pay for a box first.");
  return (await stripe().portal(customer, `${base}/account`)).url;
}

// ---- connect Hetzner and create ----

const TYPES = new Set<string>([...EU_TYPES, ...US_TYPES]);
const LOCATIONS = new Set<string>([...EU_LOCATIONS, ...US_LOCATIONS]);

async function ownBox(acct: Account, id: string) {
  const box = await q.boxFor(id, acct.id);
  if (!box) throw new ActionError("No such box.", 404);
  return box;
}

/** Checks a pasted key against Hetzner; every call it makes is recorded on the box. */
export async function checkKey(acct: Account, boxId: string, token: string) {
  const box = await ownBox(acct, boxId);
  if (box.status === "awaiting_payment" || box.status === "released") throw new ActionError("Pay for the box first.");
  return checkToken(token, (c) => q.recordCall(box.id, "check", c));
}

export type CreateInput = { token: string; name: string; serverType: string; location: string; keepKey: boolean };

export async function createBox(acct: Account, boxId: string, input: CreateInput): Promise<void> {
  const box = await ownBox(acct, boxId);
  if (box.status !== "paid" && box.status !== "failed") throw new ActionError(box.status === "awaiting_payment" ? "Pay for the box first." : "This box is already set up.");
  if (!EXTRAS_ON.has(box.plan_status)) throw new ActionError("The subscription for this box isn't active.");
  if (box.dns_state === "killed") throw new ActionError("This box was turned off after an abuse report. Write to hello@shiptiffin.com.");
  const name = String(input.name ?? "").trim().toLowerCase();
  const why = nameProblem(name);
  if (why) throw new ActionError(why);
  if (box.name && box.name !== name) throw new ActionError(`This box is called ${box.name}; its name can't change after a first try.`);
  if (!TYPES.has(input.serverType) || !LOCATIONS.has(input.location)) throw new ActionError("Pick one of the sizes and places listed.");
  const token = String(input.token ?? "").trim();
  if (!/^[A-Za-z0-9]{20,128}$/.test(token)) throw new ActionError("Paste your Hetzner token again.");
  if (await q.jobsBusy(box.id)) throw new ActionError("Setup is already running.", 409);
  const sealed = seal(kek(), token, tokenAAD(box.id));
  try {
    await q.db().begin(async (tx) => {
      const rows = await tx`update cloud_boxes set name = ${name}, server_type = ${input.serverType}, location = ${input.location},
        token_fingerprint = ${fingerprint(token)}, status = 'provisioning', updated_at = now()
        where id = ${box.id} and status in ('paid', 'failed') returning id`;
      if (rows.length === 0) throw new ActionError("This box changed meanwhile; reload the page.", 409);
      await q.enqueue(box.id, "provision", { name, serverType: input.serverType, location: input.location, keepKey: Boolean(input.keepKey), fresh: box.status === "failed" }, sealed, tx);
    });
  } catch (e: any) {
    if (e?.code === "23505") throw new ActionError(`${boxDomain(name)} is taken. Try another name.`, 409);
    throw e;
  }
}

/** Mints a one-time sign-in link on the new box (while we still hold its setup sign-in key). */
export async function openDashboard(acct: Account, boxId: string): Promise<string> {
  const box = await ownBox(acct, boxId);
  if (!box.name || box.status !== "active") throw new ActionError("The box isn't ready yet.");
  const url = dashboardUrl(box.name);
  if (!box.owner_token_sealed || (box.owner_token_expires_at && box.owner_token_expires_at < new Date())) return url;
  const owner = open(kek(), box.owner_token_sealed, ownerAAD(box.id));
  if (!owner) return url;
  try {
    const res = await fetch(`${url}/v1/login-links`, { method: "POST", headers: { Authorization: `Bearer ${owner}` }, signal: AbortSignal.timeout(10_000) });
    const body = (await res.json().catch(() => ({}))) as { url?: string };
    if (!res.ok || !body.url?.startsWith(url + "/")) return url;
    // The setup sign-in key goes a day after the first open (or at its 7 days).
    await q.db()`update cloud_boxes set first_opened_at = coalesce(first_opened_at, now()),
      owner_token_expires_at = least(owner_token_expires_at, now() + interval '1 day') where id = ${box.id}`;
    return body.url;
  } catch {
    return url;
  }
}

// ---- account actions ----

export type BoxAction =
  | { action: "forget-key" }
  | { action: "keep-key"; token: string }
  | { action: "forget-signin" }
  | { action: "resize"; serverType: string; token?: string; keepKey?: boolean }
  | { action: "cancel" }
  | { action: "resume" }
  | { action: "release"; confirm: string }
  | { action: "delete-server"; confirm: string; token: string; deleteData?: boolean };

export async function boxAction(acct: Account, boxId: string, a: BoxAction): Promise<string> {
  const box = await ownBox(acct, boxId);
  const s = q.db();
  switch (a.action) {
    case "forget-key":
      await s`update cloud_boxes set token_sealed = null, token_kept_at = null, updated_at = now() where id = ${box.id}`;
      return "Your Hetzner key is forgotten. Only its fingerprint stays.";
    case "keep-key": {
      const r = await checkToken(a.token, (c) => q.recordCall(box.id, "check", c));
      if (!r.ok) throw new ActionError(r.message);
      const token = a.token.trim();
      await s`update cloud_boxes set token_sealed = ${seal(kek(), token, tokenAAD(box.id))}, token_kept_at = now(), token_fingerprint = ${fingerprint(token)}, updated_at = now() where id = ${box.id}`;
      return "Your Hetzner key is kept, sealed. Remove it any time.";
    }
    case "forget-signin":
      await s`update cloud_boxes set owner_token_sealed = null, owner_token_expires_at = null where id = ${box.id}`;
      return "Done. We can no longer sign in to your box.";
    case "resize": {
      if (box.status !== "active") throw new ActionError("Only a running box can be resized.");
      if (!EXTRAS_ON.has(box.plan_status)) throw new ActionError("One-click resize is part of the subscription. Resize in the Hetzner console instead.");
      if (!(RESIZE_TYPES as readonly string[]).includes(a.serverType) || a.serverType === box.server_type || family(a.serverType) !== family(box.server_type ?? "")) {
        throw new ActionError("Pick another size of the same kind (Hetzner can't move a server between ARM and x86).");
      }
      if (await q.jobsBusy(box.id)) throw new ActionError("Something is already running for this box.", 409);
      const token = a.token?.trim();
      if (token) {
        if (!/^[A-Za-z0-9]{20,128}$/.test(token)) throw new ActionError("Paste your Hetzner token again.");
        await q.enqueue(box.id, "resize", { serverType: a.serverType, keepKey: Boolean(a.keepKey) }, seal(kek(), token, tokenAAD(box.id)));
      } else if (box.token_sealed) {
        await q.enqueue(box.id, "resize", { serverType: a.serverType, useStored: true }, null);
      } else throw new ActionError("Paste your Hetzner key to resize (we forgot it after setup, as you asked).");
      return "Resizing: the box is offline for about 2 minutes.";
    }
    case "cancel":
    case "resume": {
      if (!box.stripe_subscription_id) throw new ActionError("This box has no subscription.");
      await stripe().cancelAtPeriodEnd(box.stripe_subscription_id, a.action === "cancel");
      await s`update cloud_boxes set cancel_at_period_end = ${a.action === "cancel"}, updated_at = now() where id = ${box.id}`;
      return a.action === "cancel"
        ? "Cancelled at the end of this period. Your server and apps keep running after that; updates and the extras stop."
        : "The subscription continues.";
    }
    case "release":
    case "delete-server": {
      if (!box.name || a.confirm?.trim() !== box.name) throw new ActionError(`Type the box's name (${box.name ?? ""}) to confirm.`);
      if (await q.jobsBusy(box.id)) throw new ActionError("Something is already running for this box.", 409);
      if (box.stripe_subscription_id && EXTRAS_ON.has(box.plan_status)) {
        try {
          await stripe().cancelNow(box.stripe_subscription_id);
        } catch (e) {
          if (!(e instanceof StripeError && e.status === 404)) throw e;
        }
      }
      if (a.action === "delete-server") {
        const token = a.token?.trim();
        if (!token || !/^[A-Za-z0-9]{20,128}$/.test(token)) throw new ActionError("Paste a Hetzner key to delete the server: we only delete it with your key, now.");
        await q.enqueue(box.id, "delete_server", { deleteData: Boolean(a.deleteData) }, seal(kek(), token, tokenAAD(box.id)));
        return "Deleting the server in your Hetzner project.";
      }
      await s.begin(async (tx) => {
        await tx`update cloud_boxes set status = 'released', released_at = now(), token_sealed = null, token_kept_at = null,
          owner_token_sealed = null, owner_token_expires_at = null, updated_at = now() where id = ${box.id}`;
        if (box.dns_state === "live") await q.enqueue(box.id, "dns_remove", {}, null, tx);
      });
      return "Released. The server is untouched and yours; it no longer gets updates from us.";
    }
  }
}

// ---- box check-in ----

export async function heartbeat(boxId: string, report: { version?: unknown; failing?: unknown }) {
  const box = await q.boxById(boxId);
  if (!box || box.status === "released") return { managed: false, active: false, updates: true };
  const failing = Array.isArray(report.failing) ? report.failing.filter((x): x is string => typeof x === "string").slice(0, 30).map((x) => x.slice(0, 40)) : [];
  const version = typeof report.version === "string" ? report.version.slice(0, 40) : null;
  await q.db()`update cloud_boxes set last_heartbeat_at = now(), last_version = ${version}, failing = ${failing}, heartbeat_alerted_at = null where id = ${box.id}`;
  const on = EXTRAS_ON.has(box.plan_status) && box.status === "active";
  // A parked address (the box went quiet for a week) comes back with the box.
  if (on && box.dns_state === "removed" && !box.extras_paused_at && !(await q.jobsBusy(box.id))) await q.enqueue(box.id, "dns_set", {}, null);
  return {
    managed: true,
    active: on,
    updates: on,
    message: on ? undefined : "Automatic updates are paused: this box's ShipTiffin subscription is not active. Your apps keep running. Renew at shiptiffin.com/account.",
  };
}

// ---- monitor ----

export async function runMonitor(now = new Date()): Promise<{ checked: number; emails: number }> {
  const boxes = await q.db()<MonitorBox[]>`select id, name, email, status, plan_status, extras_paused_at, dns_state, last_heartbeat_at,
    health_failures, down_alerted_at, heartbeat_alerted_at, created_at from cloud_boxes where status = 'active'`;
  let emails = 0;
  await Promise.all(
    boxes.map(async (b) => {
      let d = decide(b, now);
      if (d.probe && b.name) d = decide(b, now, await probe(`${dashboardUrl(b.name)}/v1/health`));
      const p = d.patch;
      if (Object.keys(p).length) {
        await q.db()`update cloud_boxes set ${q.db()(p as any)} where id = ${b.id}`;
      }
      if (d.removeDns) await q.enqueue(b.id, "dns_remove", {}, null);
      const box = { email: b.email, name: b.name };
      const paused = b.extras_paused_at ?? now;
      const until = new Date(paused.getTime() + 30 * 86_400_000);
      for (const kind of d.emails) {
        const key = kind === "down" || kind === "up" || kind === "parked" ? (b.down_alerted_at ?? now).toISOString() : kind === "silent" ? String(b.last_heartbeat_at?.toISOString() ?? "never") : paused.toISOString();
        if (!(await q.emailOnce(b.id, kind, key))) continue;
        const m =
          kind === "down"
            ? mails.down(box, now)
            : kind === "up"
              ? mails.up(box)
              : kind === "silent"
                ? mails.silent(box, b.last_heartbeat_at)
                : kind === "dns_soon"
                  ? mails.dns_soon(box, until)
                  : kind === "parked"
                    ? mails.parked(box)
                    : mails.dns_removed(box);
        if (await deliver(m)) emails++;
      }
    }),
  );
  return { checked: boxes.length, emails };
}

// ---- abuse and admin ----

export async function reportAbuse(target: string, email: string | null, details: string): Promise<void> {
  const t = target.trim().toLowerCase().slice(0, 300);
  const m = /([a-z0-9-]+)\.shiptiffin\.app/.exec(t);
  const [box] = m ? await q.db()`select id from cloud_boxes where name = ${m[1]!}` : [];
  await q.db()`insert into cloud_abuse_reports (target, box_id, reporter_email, details) values (${t}, ${(box?.id as string) ?? null}, ${email?.slice(0, 254) || null}, ${details.slice(0, 4000)})`;
  const notify = process.env.CLOUD_ABUSE_NOTIFY?.trim() || process.env.EARLY_ACCESS_NOTIFY?.trim();
  if (notify) await deliver({ to: notify, subject: `Abuse report: ${t}`, text: `Target: ${t}\nBox: ${box?.id ?? "unknown"}\nFrom: ${email ?? "anonymous"}\n\n${details}\n\nAct on it: ${SITE}/admin` });
}

export async function adminAction(a: { action: "kill" | "restore" | "report"; boxId?: string; reason?: string; reportId?: number; status?: string }) {
  const s = q.db();
  if (a.action === "report" && a.reportId && (a.status === "acted" || a.status === "dismissed" || a.status === "new")) {
    await s`update cloud_abuse_reports set status = ${a.status} where id = ${a.reportId}`;
    return "Report updated.";
  }
  const box = a.boxId ? await q.boxById(a.boxId) : null;
  if (!box) throw new ActionError("No such box.", 404);
  if (a.action === "kill") {
    const reason = (a.reason ?? "").trim().slice(0, 500) || "abuse report";
    await s.begin(async (tx) => {
      await tx`update cloud_boxes set killed_at = now(), kill_reason = ${reason}, dns_state = 'killed', updated_at = now() where id = ${box.id}`;
      await q.enqueue(box.id, "dns_remove", { kill: true }, null, tx);
    });
    await deliver(mails.killed({ email: box.email, name: box.name }, reason));
    return `Removing ${box.name ? boxDomain(box.name) : box.id} from DNS.`;
  }
  if (a.action === "restore") {
    await s.begin(async (tx) => {
      await tx`update cloud_boxes set killed_at = null, kill_reason = null, dns_state = 'removed', updated_at = now() where id = ${box.id}`;
      if (box.status === "active") await q.enqueue(box.id, "dns_set", {}, null, tx);
    });
    return "Restoring the address.";
  }
  throw new ActionError("Unknown action.");
}
