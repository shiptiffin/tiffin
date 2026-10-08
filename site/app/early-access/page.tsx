import type { Metadata } from "next";
import { EarlyAccessForm } from "../early-access-form";
import { EarlyAccessNext } from "../early-access-next";
import { MESSAGES, type ErrorCode } from "@/lib/form";

export const metadata: Metadata = {
  title: "Early access",
  description: "Join the ShipTiffin early-access list. We let people in a few at a time, with founding prices.",
  alternates: { canonical: "/early-access" },
};

export default async function EarlyAccess({ searchParams }: { searchParams: Promise<{ error?: string }> }) {
  const { error } = await searchParams;
  const code = error && error in MESSAGES ? (error as ErrorCode) : undefined;
  return (
    <section id="form" className="ea ea-page">
      <div className="wrap ea-grid">
        <div className="ea-copy">
          <h1 className="h2">Get early access.</h1>
          <p className="section-sub">
            We&rsquo;re letting people in a few at a time. Tell us what you&rsquo;d run, and we&rsquo;ll invite you
            when there&rsquo;s a box for you.
          </p>
          <EarlyAccessNext />
        </div>
        <div className="ea-panel">
          <EarlyAccessForm initialError={code} />
        </div>
      </div>
    </section>
  );
}
