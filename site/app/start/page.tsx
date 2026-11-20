import { redirect } from "next/navigation";
// /start: sign up, connect Hetzner (the key is checked, and what it can order
// is shown with prices), pick a name, size and place, pay, watch the box being
// made, open it. Nobody pays before they have seen what their Hetzner account
// can order. Until sign-up opens (secrets set, the provisioner's tables made)
// it shows the sign-up list instead.
import type { Metadata } from "next";
import { pageMeta } from "@/lib/meta";
import { foundingCoupon, signupOpen, stripePublishable } from "@/lib/cloud/config";
import { boxesFor, foundingCount, latestJob, pendingBox, type BoxRow } from "@/lib/cloud/db";
import { FOUNDING_LIMIT } from "@/lib/cloud/stripe";
import { currentAccount } from "@/lib/cloud/session";
import { MESSAGES, type ErrorCode } from "@/lib/form";
import { SignInForm } from "../sign-in/sign-in-form";
import { Frame, StartFlow } from "./start-flow";
import { Waitlist } from "./waitlist";
import "../cloud.css";

export const metadata: Metadata = pageMeta({
  title: "Get started",
  description:
    "Set up a Tiffin box in your own Hetzner Cloud account: $19 a month per box, $12 for the first 100 customers.",
  path: "/start",
});

export const dynamic = "force-dynamic";

type Search = { error?: string; box?: string; new?: string };

export default async function Start({ searchParams }: { searchParams: Promise<Search> }) {
  const sp = await searchParams;
  if (!(await signupOpen())) {
    const code = sp.error && sp.error in MESSAGES ? (sp.error as ErrorCode) : undefined;
    return <Waitlist code={code} />;
  }
  const acct = await currentAccount();
  if (!acct) {
    return (
      <Frame
        step={0}
        title="Create your account"
        sub="Then you connect Hetzner, pick a name and a size, and pay; your box is ready about five minutes later. No password: sign in with your email, Google or GitHub."
      >
        <SignInForm next="/start" />
      </Frame>
    );
  }
  const boxes = await boxesFor(acct.id);
  const inSetup = [...boxes].reverse().find((b) => b.status === "paid" || b.status === "provisioning" || b.status === "cert_pending" || b.status === "failed");
  const justDone = sp.box ? boxes.find((b) => b.id === sp.box && b.status === "active") : undefined;
  let current: BoxRow | undefined = sp.new ? undefined : (inSetup ?? justDone);

  if (!current) {
    // Someone who already has a box and clicked Get started wants their account, not
    // a second box; adding one goes through the account's /start?new=1.
    const active = boxes.filter((b) => b.status === "active").length;
    if (active > 0 && !sp.new) redirect("/account");
    // The box-to-be: one unpaid row per account, used again until it is paid. The
    // Hetzner calls made with the customer's key before payment are recorded on it.
    current = await pendingBox(acct.id, acct.email);
  }

  const job = await latestJob(current.id, ["provision"]);
  const coupon = foundingCoupon();
  const founding = current.status === "awaiting_payment" && coupon != null && (await foundingCount()) < FOUNDING_LIMIT;
  return (
    <StartFlow
      box={{ id: current.id, name: current.name, status: current.status, fingerprint: current.token_fingerprint }}
      job={job && { status: job.status, steps: job.steps, error: job.error }}
      pay={{ publishableKey: stripePublishable(), founding }}
    />
  );
}
