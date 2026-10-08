import { Link } from "@tanstack/react-router";
import type { BoxDomain } from "@/lib/domains";

/**
 * The short version of how a domain reaches the box: whole domain or
 * subdomain (and so which record), where certificates come from, and that
 * Tiffin doesn't sell domains. Uses the box's real addresses when it has them.
 * Closed until asked for: most people only need it once.
 */
export function DomainsGuide({ box, admin }: { box?: BoxDomain; admin: boolean }) {
  const ips = box?.publicIps ?? [];
  const v4 = ips.find((a) => !a.includes(":"));
  const target = box && box.certificates === "acme" ? (box.appsDomain !== box.domain ? box.dashboard : box.domain) : "";
  const code = "ident text-[0.75rem] text-ink";
  return (
    <details className="group mt-14">
      <summary className="cursor-pointer list-none text-[0.8125rem] text-ink-3 select-none hover:text-ink [&::-webkit-details-marker]:hidden">
        <span className="inline-block transition-transform group-open:rotate-90">›</span> How domains work
      </summary>
      <div className="mt-3 grid gap-x-8 gap-y-6 border-t border-rule pt-5 text-[0.8125rem] leading-5 text-ink-2 sm:grid-cols-3">
        <div>
          <h3 className="text-[0.875rem] font-[550] text-ink">A whole domain</h3>
          <p className="mt-1">
            Like <span className={code}>example.com</span>. Point it at your box with an A record
            {v4 ? (
              <>
                {" "}
                to <span className={code}>{v4}</span>
              </>
            ) : null}
            {ips.some((a) => a.includes(":")) ? ", plus an AAAA record for its IPv6 address" : ""}. DNS hosts rarely allow a CNAME on a whole domain.
          </p>
        </div>
        <div>
          <h3 className="text-[0.875rem] font-[550] text-ink">A subdomain</h3>
          <p className="mt-1">
            Like <span className={code}>shop.example.com</span>.{" "}
            {target ? (
              <>
                One CNAME to <span className={code}>{target}</span> is enough, and it keeps working if the box’s address changes. A records work too.
              </>
            ) : (
              "It takes the same A records as a whole domain, or one CNAME to your box’s own name."
            )}
          </p>
        </div>
        <div>
          <h3 className="text-[0.875rem] font-[550] text-ink">HTTPS</h3>
          <p className="mt-1">
            Once DNS points here, the box gets a free certificate from Let’s Encrypt, usually in under a minute, and renews it about 30 days before it expires.
          </p>
        </div>
      </div>
      <p className="mt-6 max-w-[46rem] text-[0.8125rem] text-ink-3">
        Tiffin doesn’t sell or register domains. Buy one from any registrar, then add it here. Free addresses can’t take DNS records of their own, so a service that verifies by DNS needs a domain you own.
        {admin && (
          <>
            {" "}
            If Cloudflare holds a domain’s DNS,{" "}
            <Link to="/settings/dns" className="text-ink-2 underline decoration-rule-3 underline-offset-4 hover:text-ink">
              connect it
            </Link>{" "}
            and the box adds the records for you.
          </>
        )}
      </p>
    </details>
  );
}
