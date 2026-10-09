// /account: your boxes, one card each (box-card.tsx): status, address,
// subscription, the everyday changes, the log of every call we made with
// your Hetzner key, and the two ways out (stop managing, delete).
import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";
import { isAdmin } from "@/lib/cloud/config";
import { boxesFor, callsFor, latestJob, tablesReady } from "@/lib/cloud/db";
import { currentAccount } from "@/lib/cloud/session";
import { SignOut } from "./account-actions";
import { BoxCard } from "./box-card";
import "../cloud.css";

export const metadata: Metadata = { title: "Your account", robots: { index: false } };
export const dynamic = "force-dynamic";

// ?signin=asked#<box> (from Open dashboard): that box's card says a new sign-in link is on its way.
export default async function Account() {
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
        </div>
        {boxes.length === 0 && <p className="cp-sub">No boxes yet.</p>}
        {detail.map(({ b, calls, job }) => (
          <BoxCard key={b.id} b={b} calls={calls} job={job} />
        ))}
        <p className="cp-hint" style={{ marginTop: 28 }}>
          How we handle your key and what we can and can&rsquo;t do on your server: <Link href="/managed">how managed boxes work</Link>. Questions:{" "}
          <a href="mailto:hello@shiptiffin.com">hello@shiptiffin.com</a>.
        </p>
      </div>
    </section>
  );
}
