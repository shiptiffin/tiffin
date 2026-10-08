// /abuse: report a <name>.shiptiffin.app address. Customers' servers run in
// their own Hetzner accounts; the address is ours, and we remove it fast.
import type { Metadata } from "next";
import "../cloud.css";

export const metadata: Metadata = {
  title: "Report abuse",
  description: "Report phishing, malware or other abuse on a shiptiffin.app address.",
  alternates: { canonical: "/abuse" },
};

const ERRORS: Record<string, string> = {
  missing: "Say which address, and what you saw.",
  busy: "Too many reports from here just now. Email abuse@shiptiffin.com instead.",
  long: "That report is too long. Email abuse@shiptiffin.com instead.",
  unavailable: "The form isn't working right now. Email abuse@shiptiffin.com instead.",
  form: "That didn't come through. Try again, or email abuse@shiptiffin.com.",
};

export default async function Abuse({ searchParams }: { searchParams: Promise<{ sent?: string; error?: string }> }) {
  const { sent, error } = await searchParams;
  return (
    <section className="cp wrap">
      <div className="cp-col">
        <p className="kicker">Abuse</p>
        <h1>Report a shiptiffin.app address</h1>
        <p className="cp-sub">
          Every <strong>name.shiptiffin.app</strong> address belongs to a server its customer runs in their own Hetzner Cloud account.
          The address is ours: when it is used for phishing, malware or spam we take it down, usually within hours. Email works too:{" "}
          <a href="mailto:abuse@shiptiffin.com">abuse@shiptiffin.com</a>. For the server itself you can also report its IP address to{" "}
          <a href="https://abuse.hetzner.com/" rel="noreferrer">
            Hetzner
          </a>
          .
        </p>
        {sent ? (
          <div className="cp-card" role="status">
            <h2>Thanks, we have it</h2>
            <p className="cp-sub">We look at every report. If you left an email, we&rsquo;ll tell you what we did.</p>
          </div>
        ) : (
          <form className="cp-card" method="post" action="/api/abuse">
            <div className="cp-field">
              <label htmlFor="target">The address</label>
              <input id="target" name="target" type="text" required maxLength={300} placeholder="https://something.name.shiptiffin.app/…" />
            </div>
            <div className="cp-field">
              <label htmlFor="details">What&rsquo;s wrong</label>
              <textarea id="details" name="details" required maxLength={4000} placeholder="Phishing for…, malware, spam from…" />
            </div>
            <div className="cp-field">
              <label htmlFor="email">Your email (optional)</label>
              <input id="email" name="email" type="email" maxLength={254} autoComplete="email" />
            </div>
            <input type="text" name="website" tabIndex={-1} autoComplete="off" aria-hidden="true" style={{ position: "absolute", left: "-9999px" }} />
            {error && ERRORS[error] && (
              <p className="cp-err" role="alert">
                {ERRORS[error]}
              </p>
            )}
            <button className="btn btn-primary">Send the report</button>
          </form>
        )}
      </div>
    </section>
  );
}
