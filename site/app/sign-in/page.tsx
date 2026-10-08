import type { Metadata } from "next";
import { redirect } from "next/navigation";
import { safeNext } from "@/lib/cloud/names";
import { currentAccount } from "@/lib/cloud/session";
import { SignInForm } from "./sign-in-form";
import "../cloud.css";

export const metadata: Metadata = { title: "Sign in", robots: { index: false } };

export default async function SignIn({ searchParams }: { searchParams: Promise<{ next?: string }> }) {
  const next = safeNext((await searchParams).next);
  if (await currentAccount()) redirect(next);
  return (
    <section className="cp wrap">
      <div className="cp-col">
        <p className="kicker">Your account</p>
        <h1>Sign in to ShipTiffin</h1>
        <p className="cp-sub">For your boxes, billing and the log of what we did with your Hetzner key.</p>
        <SignInForm next={next} />
      </div>
    </section>
  );
}
