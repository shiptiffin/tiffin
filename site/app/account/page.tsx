// /account: your boxes, one card each (box-card.tsx): status, address,
// subscription, the everyday changes, the log of every call we made with
// your Hetzner key, and the one way out (Cancel subscription: keep the
// server, or delete it). A box being deleted shows its progress; deleted
// boxes sit at the bottom, one quiet line each.
import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";
import { isAdmin } from "@/lib/cloud/config";
import { boxesFor, callsFor, latestJob, tablesReady } from "@/lib/cloud/db";
import { currentAccount } from "@/lib/cloud/session";
import { SignOut } from "./account-actions";
import { BoxCard, DeletedList } from "./box-card";
import "../cloud.css";

export const metadata: Metadata = { title: "Your account", robots: { index: false } };
export const dynamic = "force-dynamic";

// ?signin=asked#<box> (from Open dashboard): that box's card says a new sign-in link is on its way.
export default async function Account() {
  const acct = await currentAccount();
  if (!acct) redirect("/sign-in?next=/account");
  const ready = await tablesReady();
  const all = ready ? (await boxesFor(acct.id)).filter((b) => b.status !== "awaiting_payment") : [];
  const boxes = all.filter((b) => b.status !== "deleted");
  const deleted = all.filter((b) => b.status === "deleted").sort((x, y) => (y.deleted_at?.getTime() ?? 0) - (x.deleted_at?.getTime() ?? 0));
  const detail = await Promise.all(
    boxes.map(async (b) => ({
      b,
      calls: b.status === "deleting" ? [] : await callsFor(b.id),
      job: await latestJob(b.id, b.status === "deleting" ? ["delete_server"] : undefined),
    })),
  );
  // Billing and invoices sits on a managed box's card; with none left, one quiet link for past invoices.
  const invoices = deleted.some((b) => b.stripe_customer_id) && !boxes.some((b) => b.stripe_customer_id && b.status !== "deleting");
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
        </div>
        {all.length === 0 && <p className="cp-sub">No boxes yet.</p>}
        {detail.map(({ b, calls, job }) => (
          <BoxCard key={b.id} b={b} calls={calls} job={job} />
        ))}
        {deleted.length > 0 && <DeletedList rows={deleted} invoices={invoices} />}
        <p className="cp-hint" style={{ marginTop: 32 }}>
          How we handle your key and what we can and can&rsquo;t do on your server: <Link href="/managed">how managed boxes work</Link>. Questions:{" "}
          <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>.
        </p>
      </div>
    </section>
  );
}

