// /pricing: the one plan, the Hetzner server it runs on, and how it compares with
// self-hosting the same software for free. The plan block and the bill comparison are
// the homepage's own components (../pricing.tsx, ../compare.tsx), so the two pages can't
// drift apart. Keep every fact here in step with the homepage FAQ, docs/guide/managed.md
// and the terms.
//
// Hetzner prices: read on hetzner.com on 2026-10-10 in US dollars, before VAT, with the
// primary IPv4 address included (Hetzner's cards say "Price incl. IPv4"). The 40 GB
// volume (about $2.80) is from Hetzner's API price list on 2026-10-08, as in
// compare-ledger.tsx: hetzner.com doesn't print volume prices. Re-check both, and
// HETZNER_READ, before changing a number.
import Link from "next/link";
import { pageMeta } from "@/lib/meta";
import { Answer, CtaBand, Faq, FactTable, No, PageHead, Partly, Points, Section, Yes } from "../kit";
import { Compare } from "../compare";
import { Pricing } from "../pricing";
import "./pricing-page.css";

export const dynamic = "force-static";

export const metadata = pageMeta({
  title: "Pricing: $19 a box, no usage bills",
  description:
    "ShipTiffin is $19 a month per box, plus your Hetzner server at Hetzner's prices (about $10 for the smallest). No usage charges, seats or project fees.",
  path: "/pricing",
});

const UPDATED = "2026-10-10";
const HETZNER_READ = "10 October 2026";
const VOLUME = 2.8;

const HZ_EU = "https://www.hetzner.com/cloud/cost-optimized/";
const HZ_US = "https://www.hetzner.com/cloud/regular-performance/";
const HZ_CHANGE = "https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/";

/** The sizes /start offers (site/lib/cloud/hetzner.ts), smallest first. */
const SIZES: { type: string; cpu: string; memory: string; disk: string; where: string; traffic: string; server: number }[] = [
  { type: "cx23", cpu: "2 vCPU, x86", memory: "4 GB", disk: "40 GB", where: "Germany, Finland", traffic: "20 TB", server: 7.09 },
  { type: "cax11", cpu: "2 vCPU, Arm", memory: "4 GB", disk: "40 GB", where: "Germany, Finland", traffic: "20 TB", server: 7.59 },
  { type: "cx33", cpu: "4 vCPU, x86", memory: "8 GB", disk: "80 GB", where: "Germany, Finland", traffic: "20 TB", server: 10.59 },
  { type: "cax21", cpu: "4 vCPU, Arm", memory: "8 GB", disk: "80 GB", where: "Germany, Finland", traffic: "20 TB", server: 13.09 },
  { type: "cpx21", cpu: "3 vCPU, x86", memory: "4 GB", disk: "80 GB", where: "US (Virginia, Oregon)", traffic: "2 TB", server: 38.09 },
];

const usd = (n: number) => `$${n.toFixed(2)}`;

const FAQ: { q: string; a: React.ReactNode }[] = [
  {
    q: "What do I pay, and to whom?",
    a: (
      <p>
        Two bills. ShipTiffin is $19 a month per box ($12 for our first 100 customers, locked for 24 months). Your
        server is billed by Hetzner, to you, at their prices: the smallest is about $10 a month before VAT, with its
        IPv4 address and a 40 GB data volume. Our fee never changes with traffic: there are no usage charges, seats or
        per-project fees from us.
      </p>
    ),
  },
  {
    q: "What if my apps use more traffic than Hetzner includes?",
    a: (
      <p>
        Our fee stays the same. Hetzner&rsquo;s price includes 20 TB of outgoing traffic a month in Europe (at least
        1 TB in the US, depending on the size); past that, Hetzner charges for the extra on its own bill. On{" "}
        {HETZNER_READ}, <a href={HZ_EU}>Hetzner&rsquo;s price list</a> showed $1.20 for each extra TB. Project limits
        hold memory and CPU, not traffic.
      </p>
    ),
  },
  {
    q: "Can I move to a bigger server later?",
    a: (
      <>
        <p>
          Yes. Resize from your account: it asks for a Hetzner key for that one job, changes the server type, and
          restarts the box for about 2 minutes. Hetzner can&rsquo;t move a server between x86 and Arm, so a{" "}
          <code>cx</code> server grows to a bigger <code>cx</code>, and a <code>cax</code> to a bigger{" "}
          <code>cax</code>.
        </p>
        <p>
          Our fee stays $19. Hetzner bills its current price for the new size: since 15 June 2026 its new prices apply to
          resizes as well as new orders (<a href={HZ_CHANGE}>Hetzner&rsquo;s notice</a>). Growing the data volume is
          separate: see <Link href="/docs/quickstart#hetzner">running your own server</Link>.
        </p>
      </>
    ),
  },
  {
    q: "What happens if I cancel?",
    a: (
      <p>
        Your server and your apps keep running in your Hetzner account. Updates and the managed extras stop: monitoring,
        backup copies off the server, resizing from your account, and support. Your shiptiffin.app address keeps working
        for 30 days, with an email when that starts, a week before it goes and when it goes, so you can point your own
        domain at the box. If you cancel and keep the server, the subscription runs to the end of the month you paid
        for. To stop paying Hetzner too, delete the server.
      </p>
    ),
  },
  {
    q: "Can I get a refund?",
    a: (
      <p>
        Within 14 days of your first payment, yes, in full: write to hello@shiptiffin.com. After that, the month
        already paid isn&rsquo;t refunded, as it&rsquo;s a monthly service, but you won&rsquo;t be charged again once
        you cancel. Hetzner&rsquo;s charges for your server are between you and Hetzner. The details are in our{" "}
        <Link href="/terms#refunds">terms</Link>.
      </p>
    ),
  },
  {
    q: "Do the prices include VAT or sales tax?",
    a: (
      <p>
        Hetzner&rsquo;s prices on this page are before VAT; Hetzner adds the VAT for your country on its own bill. Our
        checkout is run by Stripe as the seller of record, which works out, collects and files any sales tax or VAT due
        where you live, and shows the total before you pay.
      </p>
    ),
  },
  {
    q: "What if I need more than one box?",
    a: (
      <p>
        Each box is its own subscription at $19 a month, on its own server. Most people need one: a box holds many
        projects, each with its own limit. A second box makes sense for apps in another region, or to keep one app
        apart from the rest. Projects{" "}
        <Link href="/docs/moving">export to a file and move between boxes</Link>.
      </p>
    ),
  },
  {
    q: "Can I run it myself for free?",
    a: (
      <p>
        Yes. The software on every box is on <a href="https://github.com/shiptiffin/tiffin">GitHub</a> under the
        AGPL-3.0 licence, and the SDK your apps use is Apache-2.0. Install the <code>tiffin</code> CLI and run{" "}
        <code>tiffin up</code> against Hetzner or any Ubuntu 24.04 or 26.04 server you can SSH into. You still pay for
        the server. The steps are in{" "}
        <Link href="/guides/deploy-nextjs-to-hetzner">Deploy Next.js to Hetzner</Link>.
      </p>
    ),
  },
  {
    q: "Is there a free plan or a trial?",
    a: (
      <p>
        Not for the managed service. Instead there&rsquo;s a 14-day money-back guarantee on what you pay us. Your server
        is yours either way: if you change your mind, delete it in Hetzner and Hetzner stops billing.
      </p>
    ),
  },
  {
    q: "When is something else cheaper?",
    a: (
      <p>
        One small app on a free tier can cost less. ShipTiffin pays off from the second app. And if you need servers in
        many regions, or an app that must stay up through a hardware fault, a bigger platform is the better fit. Our{" "}
        <Link href="/compare">comparisons</Link> go through Vercel, Coolify and Supabase one by one.
      </p>
    ),
  },
];

export default function PricingPage() {
  return (
    <>
      <PageHead
        kicker="Pricing"
        title="$19 a box. No usage bills."
        crumbs={[["Pricing", "/pricing"]]}
        updated={UPDATED}
      >
        <p>
          One plan: we set up a box on a server in your own Hetzner account and look after it. Hetzner bills you for the
          server, at their prices. Or run the same open-source software yourself, free.
        </p>
      </PageHead>

      <div className="wrap pricing-answer">
        <Answer>
          <p>
            ShipTiffin costs <strong>$19 a month per box</strong> ($12 a month for the first 100 customers, locked for 24
            months). Your server is billed by Hetzner to you, at Hetzner&rsquo;s prices: on {HETZNER_READ} the smallest
            size, with its IPv4 address and a 40 GB data volume, came to about $10 a month before VAT.
          </p>
          <p>
            Our fee never changes with traffic, projects or people. Self-hosting the same software is free.
          </p>
        </Answer>
      </div>

      <Pricing />

      <Section
        id="servers"
        kicker="Your server"
        title="Hetzner server sizes a box can use"
        sub={`The sizes the setup page offers, at Hetzner's prices on ${HETZNER_READ}, in US dollars before VAT. When you set up, the page shows your own account's prices and what Hetzner has in stock.`}
        tone="sunk"
      >
        <FactTable
          caption="Hetzner Cloud server sizes for a box, with monthly prices"
          head={["Size", "CPU and memory", "Server disk", "Where", "Traffic a month", "Server with IPv4, a month", "With the 40 GB volume"]}
          rows={SIZES.map((s) => [
            <code key="t">{s.type}</code>,
            `${s.cpu}, ${s.memory}`,
            s.disk,
            s.where,
            s.traffic,
            usd(s.server),
            `about ${usd(s.server + VOLUME)}`,
          ])}
          note={
            <>
              Server prices with the IPv4 address from Hetzner&rsquo;s{" "}
              <a href={HZ_EU} rel="noopener">
                Cost-Optimized
              </a>{" "}
              and{" "}
              <a href={HZ_US} rel="noopener">
                Regular Performance
              </a>{" "}
              pages, read {HETZNER_READ}. The 40 GB data volume adds about ${VOLUME.toFixed(2)} a month (Hetzner&rsquo;s API
              price list, read 8 October 2026; its website doesn&rsquo;t list volume prices). Prices exclude VAT. Hetzner
              changed its cloud prices in 2026: from 15 June 2026 new orders and resizes pay the new prices, and servers
              ordered before keep theirs (
              <a href={HZ_CHANGE} rel="noopener">
                Hetzner&rsquo;s notice
              </a>
              ). The setup page offers only the sizes Hetzner has in stock at that moment.
            </>
          }
        />
        <div className="pricing-sizing">
          <Points
            items={[
              [
                "Start small",
                <p key="p">
                  A 4 GB server fits about 3&ndash;5 small apps, with their databases. Each project can have a limit, so
                  a busy one is held there and can&rsquo;t slow the others.
                </p>,
              ],
              [
                "x86 or Arm",
                <p key="p">
                  Both run the same software. Pick once: Hetzner can&rsquo;t move a server between them, so a box grows
                  within its family (<code>cx23</code> to <code>cx33</code>, <code>cax11</code> to <code>cax21</code>).
                </p>,
              ],
              [
                "Europe or the US",
                <p key="p">
                  Hetzner&rsquo;s US sizes cost several times more than the European ones at the moment. Choose where
                  most of your users are.
                </p>,
              ],
              [
                "Bigger later",
                <p key="p">
                  Resize from your account when the box is full. It restarts for about 2 minutes, and Hetzner bills the
                  new size&rsquo;s price. See <Link href="/docs/managed">managed boxes</Link>.
                </p>,
              ],
            ]}
          />
        </div>
      </Section>

      <Compare />

      <Section
        id="self-host"
        kicker="Managed or self-hosted"
        title="What $19 adds to the free software"
        sub="Every box runs the same open-source Tiffin, with every part. The managed service sets it up in your Hetzner account and looks after it."
      >
        <FactTable
          caption="ShipTiffin managed compared with self-hosting Tiffin for free"
          highlight={1}
          head={["", "ShipTiffin, $19 a month", "Self-host, free"]}
          rows={[
            ["Every part on the box", <Yes key="a">Apps, Postgres, KV, files, sign-in, email, jobs, analytics, error tracking</Yes>, <Yes key="b">The same</Yes>],
            ["Where the server runs", "Your Hetzner account, billed by Hetzner", "Your Hetzner account, or any Ubuntu 24.04 or 26.04 server you can SSH into"],
            ["Setup", "In the browser, about 5 minutes; nothing to install", <>From your computer with <code>tiffin up</code>, a few minutes</>],
            ["Tiffin updates", <Yes key="a">Installed by the box, checked about hourly</Yes>, <Yes key="b">The same signed releases, installed by the box</Yes>],
            ["Backups on the server", <Yes key="a">Postgres to any second of the last 7 days</Yes>, <Yes key="b">The same</Yes>],
            ["Backups off the server", <Yes key="a">Every 6 hours, encrypted with a key only you hold, kept 30 days</Yes>, <Partly key="b">To an S3-compatible bucket you set up</Partly>],
            ["Monitoring from outside the box", <Yes key="a">An email when the box stops answering</Yes>, <No key="b" />],
            ["Address with HTTPS", "A free name.shiptiffin.app", "A free name under sslip.io, until you set your own domain"],
            ["Bigger server", "From your account", <><code>tiffin up --name shop --type cax21</code></>],
            ["Your Hetzner key", "Used to build the box, then forgotten", "Read from your shell when you run tiffin up"],
            ["Support", <Yes key="a">By email, from the people who build it</Yes>, <No key="b">The docs and the code</No>],
          ]}
          note={
            <>
              From <Link href="/docs/managed">Managed boxes</Link> and the{" "}
              <Link href="/docs/quickstart">quickstart</Link>. The code is at{" "}
              <a href="https://github.com/shiptiffin/tiffin">github.com/shiptiffin/tiffin</a> (AGPL-3.0; the SDK is
              Apache-2.0). More on <Link href="/open-source">running Tiffin yourself</Link>.
            </>
          }
        />
      </Section>

      <Section
        id="founding"
        kicker="Founding offer"
        title="$12 a month for the first 100 customers"
        sub="The same box and the same extras, for less. Tiffin is pre-1.0, and our first customers pay less for being early."
        tone="sunk"
      >
        <Points
          items={[
            ["$12 a month per box", <p key="p">Instead of $19, for our first 100 customers.</p>],
            ["Locked for 24 months", <p key="p">From when you subscribe. It doesn&rsquo;t go up in that time.</p>],
            [
              "Same terms",
              <p key="p">Monthly, cancel any time, and the 14-day money-back guarantee on your first payment.</p>,
            ],
            [
              "If you leave and come back",
              <p key="p">
                Renewing a box later starts a new subscription at $19 a month; the founding price doesn&rsquo;t carry
                over.
              </p>,
            ],
          ]}
        />
      </Section>

      <Section id="faq" title="Pricing questions">
        <Faq items={FAQ} />
      </Section>

      <CtaBand title="One box, every app on it">
        $19 a month, plus your server at Hetzner&rsquo;s price. Set up in about five minutes.
      </CtaBand>
    </>
  );
}
