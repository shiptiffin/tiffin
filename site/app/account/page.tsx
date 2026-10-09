// /account: your boxes (status, plan, address, open), billing, the log of
// every call we made with your Hetzner key, resize, cancel, release and
// delete.
import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";
import { renewable } from "@/lib/cloud/actions";
import { DNS_GRACE_DAYS, extrasOn } from "@/lib/cloud/billing";
import { isAdmin } from "@/lib/cloud/config";
import { boxesFor, callsFor, latestJob, tablesReady, type BoxRow } from "@/lib/cloud/db";
import { family, RESIZE_TYPES } from "@/lib/cloud/hetzner";
import { boxDomain, dashboardUrl } from "@/lib/cloud/names";
import { currentAccount } from "@/lib/cloud/session";
import { BillingButton, BoxActions, SignOut } from "./account-actions";
import "../cloud.css";

export const metadata: Metadata = { title: "Your account", robots: { index: false } };
export const dynamic = "force-dynamic";

const when = (d: Date | null) => (d ? d.toLocaleString("en-GB", { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" }) + " UTC" : "never");
const day = (d: Date | null) => (d ? d.toLocaleDateString("en-GB", { dateStyle: "medium", timeZone: "UTC" }) : "");

function statusPill(b: BoxRow): [string, "good" | "warn" | "bad" | undefined] {
  if (b.attention && b.status !== "released") return ["Needs attention", "bad"];
  switch (b.status) {
    case "active":
      if (b.dns_state === "killed") return ["Address turned off", "bad"];
      if (b.dns_state === "parked") return ["Address parked: no check-in", "warn"];
      if (b.down_alerted_at) return ["Not answering", "bad"];
      return ["Running", "good"];
    case "cert_pending":
      return ["Installed: certificate pending", "warn"];
    case "provisioning":
      return ["Being created", "warn"];
    case "deleting":
      return ["Being deleted", "warn"];
    case "failed":
      return ["Setup stopped", "bad"];
    case "paid":
      return ["Waiting for Hetzner", "warn"];
    case "awaiting_payment":
      return ["Not paid", undefined];
    case "released":
      return ["Released", undefined];
  }
}

function planWords(b: BoxRow): string {
  if (b.status === "released") return "No subscription. The server is yours, unmanaged.";
  if (b.plan_status === "none") return "Not paid yet";
  const price = b.founding ? "$12 a month (founding price)" : "$19 a month";
  if (b.refunded_at) return "Refunded; the subscription has ended.";
  if (extrasOn(b.plan_status, Boolean(b.first_paid_at))) {
    if (b.cancel_at_period_end) return `${price}, ends ${day(b.current_period_end)}`;
    if (b.plan_status === "past_due") return `${price}, payment overdue: update your card`;
    return b.current_period_end ? `${price}, renews ${day(b.current_period_end)}` : price;
  }
  const until = b.extras_paused_at ? new Date(b.extras_paused_at.getTime() + DNS_GRACE_DAYS * 86_400_000) : null;
  return `Ended (${b.plan_status}). Updates and extras are paused${b.dns_state === "live" && until ? `; the address stays until ${day(until)}` : ""}.`;
}

export default async function Account({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const asked = (await searchParams).signin === "asked";
  const acct = await currentAccount();
  if (!acct) redirect("/sign-in?next=/account");
  const ready = await tablesReady();
  const boxes = ready ? (await boxesFor(acct.id)).filter((b) => b.status !== "awaiting_payment") : [];
  const detail = await Promise.all(boxes.map(async (b) => ({ b, calls: await callsFor(b.id), job: await latestJob(b.id) })));
  return (
    <section className="cp wrap">
      <div className="cp-col">
        <div className="cp-row spread">
          <p className="kicker" style={{ margin: 0 }}>
            Signed in as {acct.email}
          </p>
          <div className="cp-row">
            {isAdmin(acct) && <Link href="/admin">Admin</Link>}
            <SignOut />
          </div>
        </div>
        <h1>Your boxes</h1>
        <div className="cp-row" style={{ marginTop: 16 }}>
          <Link className="btn btn-primary" href="/start?new=1">
            {boxes.length ? "Add a box" : "Set up a box"}
          </Link>
          {boxes.some((b) => b.stripe_customer_id) && <BillingButton />}
        </div>
        {boxes.length === 0 && <p className="cp-sub">No boxes yet.</p>}
        {asked && (
          <p className="cp-hint" role="status">
            We asked your box for a new one-time sign-in link. It makes it at its next check-in, usually within a few minutes. Then click Open dashboard again.
          </p>
        )}
        {detail.map(({ b, calls, job }) => {
          const [label, tone] = statusPill(b);
          return (
            <article key={b.id} className="cp-card" id={b.id}>
              <div className="cp-box-head">
                <h2>{b.name ?? "New box"}</h2>
                <span className="cp-pill" data-tone={tone}>
                  {label}
                </span>
              </div>
              <dl className="cp-facts">
                {b.name && (
                  <>
                    <dt>Address</dt>
                    <dd>
                      {b.dns_state === "live" ? (
                        <a href={dashboardUrl(b.name)}>{boxDomain(b.name)}</a>
                      ) : (
                        `${boxDomain(b.name)} (${b.dns_state === "killed" ? "turned off" : b.dns_state === "parked" ? "parked: it comes back at the box's next check-in" : "not live"})`
                      )}
                    </dd>
                  </>
                )}
                <dt>Plan</dt>
                <dd>{planWords(b)}</dd>
                {b.server_type && (
                  <>
                    <dt>Server</dt>
                    <dd>
                      Hetzner {b.server_type} in {b.location}
                      {b.ipv4 ? `, ${b.ipv4}` : ""}
                    </dd>
                  </>
                )}
                {(b.status === "active" || b.status === "cert_pending") && (
                  <>
                    <dt>Last check-in</dt>
                    <dd>
                      {when(b.last_heartbeat_at)}
                      {b.last_version ? `, Tiffin ${b.last_version}` : ""}
                      {b.failing.length ? `, failing: ${b.failing.join(", ")}` : ""}
                    </dd>
                  </>
                )}
                <dt>Hetzner key</dt>
                <dd>
                  Not kept: each change that needs it asks for it and forgets it when done
                  {b.token_fingerprint ? ` (setup used the one with fingerprint ${b.token_fingerprint})` : ""}. You can delete it in
                  Hetzner (Security › API tokens) at any time; the box keeps running.
                </dd>
                {b.status === "active" && !b.handoff_closed_at && (
                  <>
                    <dt>Sign-in link</dt>
                    <dd>
                      {b.signin_code && b.signin_expires_at && b.signin_expires_at > new Date()
                        ? <>We hold the one-time sign-in link your box made, so &ldquo;Open dashboard&rdquo; signs you in. It works once, and your box refuses it after {when(b.signin_expires_at)}. We forget it once your box tells us you signed in.</>
                        : b.signin_requested_at
                          ? "We asked your box for a new one-time sign-in link; it arrives at its next check-in."
                          : "The sign-in link your box made has expired. Ask it for a new one below, or sign in on the box itself."}
                    </dd>
                  </>
                )}
              </dl>
              {b.attention && b.status !== "released" && (
                <p className="cp-err" role="status">
                  {b.attention} We&rsquo;ve been told too; write to <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a> with questions.
                </p>
              )}
              {job && (job.status === "queued" || job.status === "running" || (job.status === "failed" && job.kind !== "provision")) && (
                <p className={job.status === "failed" ? "cp-err" : "cp-hint"} role="status">
                  {job.kind.replace("_", " ")}: {job.status === "failed" ? job.error : (job.steps.at(-1)?.text ?? job.status)}
                </p>
              )}
              <BoxActions
                box={{
                  id: b.id,
                  name: b.name,
                  status: b.status,
                  active: extrasOn(b.plan_status, Boolean(b.first_paid_at)),
                  renewable: renewable(b),
                  cancelAtPeriodEnd: b.cancel_at_period_end,
                  hasSubscription: Boolean(b.stripe_subscription_id),
                  signinLink: Boolean(b.signin_code),
                  handoffOpen: b.status === "active" && !b.handoff_closed_at,
                  serverType: b.server_type,
                  sizes: RESIZE_TYPES.filter((t) => b.server_type && t !== b.server_type && family(t) === family(b.server_type)),
                }}
              />
              <details>
                <summary>Hetzner calls we made with your key ({calls.length}{calls.length === 200 ? "+" : ""})</summary>
                {calls.length === 0 ? (
                  <p className="cp-hint">None yet.</p>
                ) : (
                  <div className="cp-scroll cp-table-wrap">
                    <table className="cp-log">
                      <thead>
                        <tr>
                          <th>When (UTC)</th>
                          <th>Why</th>
                          <th>Request</th>
                          <th>Answer</th>
                        </tr>
                      </thead>
                      <tbody>
                        {calls.map((c, i) => (
                          <tr key={i}>
                            <td>{c.at.toISOString().replace("T", " ").slice(0, 19)}</td>
                            <td>{c.purpose}</td>
                            <td>
                              {c.method} {c.path}
                            </td>
                            <td>{c.status ?? c.error ?? "no answer"}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
                <p className="cp-hint">The key itself is never logged. Hetzner keeps its own record under Security → API tokens.</p>
              </details>
            </article>
          );
        })}
        <p className="cp-hint" style={{ marginTop: 28 }}>
          How we handle your key and what we can and can&rsquo;t do on your server: <Link href="/managed">how managed boxes work</Link>. Questions:{" "}
          <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>.
        </p>
      </div>
    </section>
  );
}
