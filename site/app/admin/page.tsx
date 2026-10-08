// /admin: owner only (account ids in CLOUD_ADMIN_USER_IDS, verified email). Boxes at a glance, abuse reports
// and the kill switch that removes a box's shiptiffin.app records. For
// everything else, the website project's Database browser has the tables.
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { isAdmin } from "@/lib/cloud/config";
import { db, tablesReady } from "@/lib/cloud/db";
import { boxDomain } from "@/lib/cloud/names";
import { currentAccount } from "@/lib/cloud/session";
import { AdminButton } from "./admin-actions";
import "../cloud.css";

export const metadata: Metadata = { title: "Admin", robots: { index: false } };
export const dynamic = "force-dynamic";

export default async function Admin() {
  const acct = await currentAccount();
  if (!isAdmin(acct)) notFound();
  if (!(await tablesReady())) return <p className="wrap cp">The cloud worker hasn&rsquo;t made its tables yet.</p>;
  const s = db();
  const boxes = await s`select id, name, email, status, plan_status, dns_state, founding, server_type, location, ipv4, last_heartbeat_at, last_version, kill_reason, attention,
    first_paid_at, refunded_at, stripe_subscription_id, heartbeat_refused_at, heartbeat_refused_why, created_at
    from cloud_boxes where status <> 'awaiting_payment' order by created_at desc limit 300`;
  const stuck = await s`select box_id, kind, key, attempts, status, last_error, created_at from cloud_outbox where status = 'failed' or (status = 'queued' and attempts > 2)
    order by id desc limit 50`;
  const billing = await s`select box_id, what, subscription_id, at from cloud_billing_log order by id desc limit 30`;
  const reports = await s`select id, target, box_id, reporter_email, details, status, created_at from cloud_abuse_reports order by (status = 'new') desc, created_at desc limit 100`;
  const [counts] = await s`select (select count(*) from cloud_founding_claims)::int as founding, (select count(*) from cloud_boxes where status = 'active')::int as active,
    (select count(*) from cloud_jobs where status in ('queued', 'running'))::int as jobs, (select count(*) from cloud_jobs where status = 'failed' and finished_at > now() - interval '1 day')::int as failed`;
  return (
    <section className="cp wrap">
      <p className="kicker">Admin</p>
      <h1>Boxes and reports</h1>
      <p className="cp-sub">
        {counts?.active} active boxes · {counts?.founding} of 100 founding prices used · {counts?.jobs} jobs waiting or running · {counts?.failed} failed in the last day
      </p>
      <div className="cp-card">
        <h2>Abuse reports</h2>
        {reports.length === 0 && <p className="cp-hint">None.</p>}
        {reports.map((r) => (
          <div key={r.id} style={{ borderTop: "1px solid var(--rule)", paddingTop: 10 }}>
            <p>
              <strong>{r.target}</strong> <span className="cp-pill">{r.status}</span> <span className="cp-muted">{new Date(r.created_at).toISOString().slice(0, 16)} · {r.reporter_email ?? "anonymous"} · box {r.box_id ?? "unknown"}</span>
            </p>
            <p className="cp-muted" style={{ whiteSpace: "pre-wrap" }}>{r.details}</p>
            <div className="cp-row">
              {r.box_id && <AdminButton body={{ action: "kill", boxId: r.box_id }} label="Kill the address" ask="Remove this box's shiptiffin.app records? The customer is emailed." reason />}
              <AdminButton body={{ action: "report", reportId: Number(r.id), status: "acted" }} label="Mark acted" />
              <AdminButton body={{ action: "report", reportId: Number(r.id), status: "dismissed" }} label="Dismiss" />
            </div>
          </div>
        ))}
      </div>
      {stuck.length > 0 && (
        <div className="cp-card cp-table-wrap">
          <h2>Outbox: failing</h2>
          <table className="cp-log">
            <tbody>
              {stuck.map((o, i) => (
                <tr key={i}>
                  <td>{o.box_id}</td>
                  <td>
                    {o.kind} {o.key}
                  </td>
                  <td>
                    {o.status} after {o.attempts} tries: {o.last_error}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {billing.length > 0 && (
        <div className="cp-card cp-table-wrap">
          <h2>Billing log</h2>
          <table className="cp-log">
            <tbody>
              {billing.map((l, i) => (
                <tr key={i}>
                  <td>{new Date(l.at).toISOString().slice(0, 16)}</td>
                  <td>{l.box_id}</td>
                  <td>
                    {l.what} {l.subscription_id}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="cp-card cp-table-wrap">
        <h2>Boxes</h2>
        <table className="cp-log">
          <thead>
            <tr>
              <th>Box</th>
              <th>Customer</th>
              <th>Status</th>
              <th>Server</th>
              <th>Check-in</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {boxes.map((b) => (
              <tr key={b.id}>
                <td>
                  {b.name ? boxDomain(b.name) : "(no name)"}
                  <br />
                  <span className="cp-muted">{b.id}</span>
                </td>
                <td>{b.email}</td>
                <td>
                  {b.status} · {b.plan_status}
                  {b.founding ? " · founding" : ""} · dns {b.dns_state}
                  {b.kill_reason ? ` (${b.kill_reason})` : ""}
                  {b.attention && (
                    <>
                      <br />
                      <strong>Needs attention:</strong> {b.attention}
                    </>
                  )}
                  <br />
                  <span className="cp-muted">
                    {b.first_paid_at ? `first paid ${Math.floor((Date.now() - new Date(b.first_paid_at).getTime()) / 86_400_000)} days ago` : "not paid"}
                    {b.refunded_at ? " · refunded" : ""}
                  </span>
                </td>
                <td>
                  {b.server_type} {b.location} {b.ipv4}
                </td>
                <td>
                  {b.last_heartbeat_at ? new Date(b.last_heartbeat_at).toISOString().slice(0, 16) : "never"} {b.last_version}
                  {b.heartbeat_refused_at && (
                    <>
                      <br />
                      <span className="cp-muted">
                        refused {new Date(b.heartbeat_refused_at).toISOString().slice(0, 16)}: {b.heartbeat_refused_why}
                      </span>
                    </>
                  )}
                </td>
                <td>
                  {b.dns_state === "killed" ? (
                    <AdminButton body={{ action: "restore", boxId: b.id }} label="Restore" ask="Point the address at the box again?" />
                  ) : b.dns_state === "live" || b.dns_state === "pending" ? (
                    <AdminButton body={{ action: "kill", boxId: b.id }} label="Kill" ask="Remove this box's shiptiffin.app records? The customer is emailed." reason />
                  ) : null}
                  {b.attention && <AdminButton body={{ action: "resolve", boxId: b.id }} label="Seen to" ask="Clear this box's attention note?" />}
                  {b.stripe_subscription_id && b.first_paid_at && !b.refunded_at && (
                    <AdminButton
                      body={{ action: "refund", boxId: b.id }}
                      label="Refund and cancel"
                      ask="The 14-day money-back: refund the first payment in full and end the subscription now? The customer is emailed."
                    />
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
