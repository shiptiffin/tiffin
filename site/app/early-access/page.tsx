import type { Metadata } from "next";
import { EarlyAccessForm } from "../early-access-form";
import { InviteSteps } from "../early-access-next";
import { MESSAGES, type ErrorCode } from "@/lib/form";

export const metadata: Metadata = {
  title: "Request an invite",
  description:
    "Request a ShipTiffin invite. We let people in in small groups so every box gets attention; invites go out weekly, with founding prices.",
  alternates: { canonical: "/early-access" },
};

export default async function RequestInvite({ searchParams }: { searchParams: Promise<{ error?: string }> }) {
  const { error } = await searchParams;
  const code = error && error in MESSAGES ? (error as ErrorCode) : undefined;
  return (
    <section id="form" className="ea ea-page">
      <div className="wrap ea-grid">
        <div className="ea-copy">
          <p className="kicker">Invite only</p>
          <h1 className="h2">Request an invite.</h1>
          <p className="section-sub">
            We&rsquo;re letting people in in small groups so every box gets attention. Invites go out weekly.
          </p>
          <InviteSteps />
        </div>
        <div className="ea-panel">
          <EarlyAccessForm initialError={code} />
        </div>
      </div>
    </section>
  );
}
