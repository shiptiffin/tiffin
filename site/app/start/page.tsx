// /start: sign up, pay, connect Hetzner, pick a name, size and place, watch
// the box being made, open it. Until sign-up opens (secrets set, the
// provisioner's tables made) it shows the sign-up list instead.
import type { Metadata } from "next";
import Link from "next/link";
import { confirmCheckout } from "@/lib/cloud/actions";
import { foundingCoupon, signupOpen } from "@/lib/cloud/config";
import { boxesFor, foundingCount, latestJob, type BoxRow } from "@/lib/cloud/db";
import { FOUNDING_LIMIT } from "@/lib/cloud/stripe";
import { currentAccount } from "@/lib/cloud/session";
import { MESSAGES, type ErrorCode } from "@/lib/form";
import { SignInForm } from "../sign-in/sign-in-form";
import { PayButton, StartFlow, Steps, WaitForPayment } from "./start-flow";
import { Waitlist } from "./waitlist";
import "../cloud.css";

export const metadata: Metadata = {
  title: "Get started",
  description: "Set up a Tiffin box in your own Hetzner Cloud account: $19 a month per box, $12 for the first 100 customers.",
  alternates: { canonical: "/start" },
};

export const dynamic = "force-dynamic";

type Search = { error?: string; box?: string; session_id?: string; canceled?: string; new?: string };

export default async function Start({ searchParams }: { searchParams: Promise<Search> }) {
  const sp = await searchParams;
  if (!(await signupOpen())) {
    const code = sp.error && sp.error in MESSAGES ? (sp.error as ErrorCode) : undefined;
    return <Waitlist code={code} />;
  }
  const acct = await currentAccount();
  if (!acct) {
    return (
      <Shell
        step={0}
        title="Set up your box"
        sub="A Tiffin box in your own Hetzner Cloud account, ready in about five minutes. First, an account: no password, just your email, Google or GitHub."
      >
        <SignInForm next="/start" />
      </Shell>
    );
  }
  if (sp.box && sp.session_id) await confirmCheckout(acct, sp.box, sp.session_id);
  const boxes = await boxesFor(acct.id);
  const inSetup = [...boxes].reverse().find((b) => b.status === "paid" || b.status === "provisioning" || b.status === "cert_pending" || b.status === "failed");
  const justDone = sp.box ? boxes.find((b) => b.id === sp.box && b.status === "active") : undefined;
  const current: BoxRow | undefined = sp.new ? undefined : (inSetup ?? justDone);

  if (current) {
    const job = await latestJob(current.id, ["provision"]);
    return (
      <Shell
        step={current.status === "active" ? 4 : current.status === "provisioning" || current.status === "cert_pending" ? 3 : 2}
        title={current.status === "active" ? "Your box is ready" : "Set up your box"}
      >
        <StartFlow
          box={{ id: current.id, name: current.name, status: current.status, fingerprint: current.token_fingerprint }}
          job={job && { status: job.status, steps: job.steps, error: job.error }}
        />
      </Shell>
    );
  }

  const waiting = sp.box ? boxes.find((b) => b.id === sp.box && b.status === "awaiting_payment") : undefined;
  if (waiting && sp.session_id) {
    return (
      <Shell step={1} title="Confirming your payment">
        <WaitForPayment />
      </Shell>
    );
  }

  const coupon = foundingCoupon();
  const founding = coupon != null && (await foundingCount()) < FOUNDING_LIMIT;
  const active = boxes.filter((b) => b.status === "active").length;
  return (
    <Shell
      step={1}
      title={active ? "Add another box" : "Set up your box"}
      sub={active ? `You have ${active} box${active > 1 ? "es" : ""} already. Each box is its own subscription.` : undefined}
    >
      <div className="cp-card">
        <div>
          <h2>{founding ? "$12 a month, locked for 24 months" : "$19 a month per box"}</h2>
          {founding && <p className="cp-muted cp-price-note">Founding price for our first 100 customers. $19 a month after that.</p>}
        </div>
        <ul className="cp-perks">
          <li>Tiffin installed on your own server, then kept up to date</li>
          <li>Monitoring from outside, with an email if your box stops answering</li>
          <li>
            A free <strong>name.shiptiffin.app</strong> address with HTTPS
          </li>
          <li>One-click resize, and support by email</li>
        </ul>
        <ul className="cp-facts">
          <li>Hetzner bills the server itself: about €5 to €7 a month.</li>
          <li>Cancel any time. Your server and apps keep running.</li>
          <li>14-day money-back guarantee.</li>
        </ul>
        {sp.canceled && <p className="cp-hint">Payment cancelled; nothing was charged.</p>}
        <PayButton />
        <p className="cp-hint">
          Payment by Stripe. Signed in as {acct.email}. <Link href="/account">Your account</Link>
        </p>
      </div>
    </Shell>
  );
}

function Shell({ step, title, sub, children }: { step: number; title: string; sub?: string; children: React.ReactNode }) {
  return (
    <section className="cp wrap">
      <div className="cp-col">
        <Steps now={step} />
        <h1>{title}</h1>
        {sub && <p className="cp-sub">{sub}</p>}
        {children}
      </div>
    </section>
  );
}
