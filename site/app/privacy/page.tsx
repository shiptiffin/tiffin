import type { Metadata } from "next";
import Link from "next/link";
import { EMAIL } from "../chrome";
import { LegalPage, type Section } from "../legal";

export const metadata: Metadata = {
  title: "Privacy policy",
  description:
    "What ShipTiffin collects, why, how it uses Google user data from Sign in with Google, who helps us run it, and how to have it deleted.",
  alternates: { canonical: "/privacy" },
};

const mail = <a href={`mailto:${EMAIL}`}>{EMAIL}</a>;

const sections: Section[] = [
  {
    id: "who",
    title: "Who we are",
    body: (
      <>
        <p>
          ShipTiffin runs Tiffin servers (&ldquo;boxes&rdquo;) for customers: one server per customer, with a
          dashboard at dashboard.shiptiffin.com and apps at addresses under shiptiffin.app or the customer&rsquo;s
          own domains. In this policy &ldquo;ShipTiffin&rdquo;, &ldquo;we&rdquo; and &ldquo;us&rdquo; mean the
          operator of this service, and &ldquo;you&rdquo; means anyone who uses it or visits this website.
        </p>
        <p>Questions about privacy go to {mail}. A person reads that inbox.</p>
      </>
    ),
  },
  {
    id: "collect",
    title: "What we collect",
    body: (
      <>
        <h3>Your account</h3>
        <p>
          Your email address and your name, so you can sign in and we can reach you about your box. If you sign
          in with a passkey, your device keeps the private key; we store only the public key and a counter.
        </p>
        <h3>How your box is used</h3>
        <p>
          Measurements of the box and its projects: CPU, memory, disk and network use, how many requests each app
          serves and how fast, whether services are healthy. We use them to run the service, plan capacity and,
          once pricing exists, to bill you.
        </p>
        <h3>Logs</h3>
        <p>
          Your box keeps logs: what its own services print, what your apps print, and a line for each request
          that reaches an app (time, method, the path without its query string, status, duration, and the
          visitor&rsquo;s IP address and browser). It also keeps short traces of slow or failing requests, and a
          record of mail your apps send. Sign-in tokens and keys are masked before a line is stored. These logs
          stay on your box. We read them only to keep the service running, to answer a question you ask us, or
          to look into abuse.
        </p>
        <h3>Data in your apps</h3>
        <p>
          Everything your apps store (database rows, files, KV keys, the accounts of people who sign in to your
          apps) lives on your box. Mail your apps send goes out through the mail provider you connect to your box,
          under your agreement with them. You decide what goes there. For that data we act on your
          behalf: we don&rsquo;t look at it, use it or share it, except to keep the service running, when you ask
          us to help, to investigate abuse of the service, or when the law requires it.
        </p>
        <h3>When you write to us</h3>
        <p>What you send us, and our replies.</p>
        <h3>This website</h3>
        <p>
          shiptiffin.com runs on a Tiffin box. It counts visits the way every Tiffin box does: without cookies or
          scripts, from the request itself. A visitor is a hash of the IP address and browser with a salt that
          changes daily and is deleted after 48 hours; the IP address and browser are not stored with the count.
          Browsers that send Global Privacy Control are not counted.
        </p>
      </>
    ),
  },
  {
    id: "google",
    title: "Google user data",
    body: (
      <>
        <p>
          This section covers the information ShipTiffin receives from Google when you use Sign in with Google.
          It is the only reason ShipTiffin uses Google APIs.
        </p>

        <h3 id="google-access">What we access</h3>
        <p>
          ShipTiffin asks Google for three sign-in scopes, and no others:
        </p>
        <ul>
          <li>
            <code>openid</code>: your Google account ID, so we can recognise you when you sign in again.
          </li>
          <li>
            <code>email</code>: your email address, and whether Google has verified it.
          </li>
          <li>
            <code>profile</code>: your name and profile picture.
          </li>
        </ul>
        <p>
          We do not ask for, and cannot reach, your Gmail, Google Drive, Google Calendar, contacts, YouTube or any
          other Google data.
        </p>
        <p>Sign in with Google is offered in two places:</p>
        <ul>
          <li>
            <strong>Your box&rsquo;s dashboard.</strong> People who already have access to a box can sign in to
            its dashboard with Google.
          </li>
          <li>
            <strong>Apps hosted on ShipTiffin.</strong> An app&rsquo;s owner can turn on ShipTiffin&rsquo;s shared
            Sign in with Google for their app. Google&rsquo;s consent screen then names ShipTiffin, and your
            information goes to that app&rsquo;s user accounts on the box that runs it.
          </li>
        </ul>

        <h3 id="google-use">How we use it</h3>
        <p>We use Google user data only to sign you in:</p>
        <ul>
          <li>
            <strong>Dashboard:</strong> we use your verified email address to find the person with that address on
            the box and sign them in. Signing in with Google never creates a dashboard account; if nobody on the
            box has that address, sign-in fails.
          </li>
          <li>
            <strong>Apps:</strong> the app uses your account ID, name, email address and picture to create or find
            your account in that app, and to show your name and picture to you there.
          </li>
        </ul>
        <p>
          We do not use it for anything else: not for marketing email, profiling, advertising, or to build other
          products.
        </p>

        <h3 id="google-store">How we store and protect it</h3>
        <ul>
          <li>
            <strong>Where it lives:</strong> on the customer&rsquo;s own box, a server that runs for that one
            customer only, in Hetzner&rsquo;s data centres. It is not copied to a central ShipTiffin database.
          </li>
          <li>
            <strong>Dashboard sign-in keeps nothing new.</strong> The token Google returns is used once to read
            your email address, then thrown away.
          </li>
          <li>
            <strong>App sign-in</strong> stores your account ID, name, email address and picture in the app&rsquo;s
            user accounts in the box&rsquo;s database, with the tokens Google returns at sign-in. Those tokens are
            stored encrypted on the box, give access only to the same basic profile, and the access token expires
            after an hour.
          </li>
          <li>
            <strong>Encryption:</strong> all traffic to and from a box uses HTTPS (TLS). The sign-in tokens Google
            returns (access, refresh and ID tokens) are encrypted before they are stored on the box, with a key
            kept outside the app&rsquo;s database and out of its code. The secret keys for ShipTiffin&rsquo;s
            Google sign-in are encrypted on the box. Backup copies are encrypted before they leave the box.
          </li>
          <li>
            <strong>Access controls:</strong> databases are not reachable from the internet. On a box, an
            app&rsquo;s data can be reached only by that app&rsquo;s code and by the people the box&rsquo;s owner
            gives access to. The dashboard needs a signed-in session, and API keys are limited in what they can do
            and stored only as hashes. At ShipTiffin, only the people who run the service can reach a box, and
            only for the reasons in <a href="#google-share">Who we share it with</a>.
          </li>
          <li>
            <strong>Separation:</strong> every customer has their own server, so one customer&rsquo;s data never
            shares a machine with another&rsquo;s.
          </li>
        </ul>

        <h3 id="google-share">Who we share it with</h3>
        <p>We do not share Google user data with anyone, except:</p>
        <ul>
          <li>
            The app you chose to sign in to, and its owner. The owner looks after your information under their own
            privacy policy, and our <Link href="/terms#use">terms</Link> require them to follow Google&rsquo;s
            rules for it.
          </li>
          <li>
            The companies that process data for us only as needed to run the service: Hetzner (hosting the servers
            the data is stored on), Cloudflare (DNS) and SendGrid (delivering ShipTiffin&rsquo;s own email, such as
            notices to your email address). See <a href="#providers">Who helps us run it</a>.
          </li>
          <li>When the law requires it, or to protect the security of the service and the people who use it.</li>
          <li>With your explicit consent.</li>
        </ul>
        <p>
          We never sell, rent or trade Google user data, and never pass it to advertising platforms, data brokers
          or information resellers.
        </p>

        <h3 id="google-delete">Retention and deletion</h3>
        <ul>
          <li>
            We keep Google user data for as long as your account exists: your dashboard account, or your account
            in the app you signed in to.
          </li>
          <li>
            When an account is removed, the Google user data stored with it is deleted. When a box is closed, its
            server and disks are deleted with everything on them, and encrypted backup copies expire within 30
            days.
          </li>
          <li>
            To have your Google user data deleted, email {mail} from the address you signed in with, and tell us
            which dashboard or app it was. We delete it within 30 days and tell you when it is done.
          </li>
          <li>
            If you signed in to an app hosted on ShipTiffin, you can also ask the app&rsquo;s owner. If you
            can&rsquo;t reach them, write to us and we will make sure it is deleted.
          </li>
          <li>
            You can remove ShipTiffin&rsquo;s access at any time in your Google Account, under{" "}
            <a href="https://myaccount.google.com/connections">Third-party apps and services</a>.
          </li>
        </ul>

        <h3 id="google-limited-use">Limited Use</h3>
        <div className="callout">
          <p>
            ShipTiffin's use and transfer of information received from Google APIs to any other app will adhere to{" "}
            <a href="https://developers.google.com/terms/api-services-user-data-policy">
              Google API Services User Data Policy
            </a>
            , including the{" "}
            <a href="https://developers.google.com/terms/api-services-user-data-policy#additional_requirements_for_specific_api_scopes">
              Limited Use requirements
            </a>
            .
          </p>
        </div>

        <h3 id="google-ai">No AI training, no advertising, no sale</h3>
        <ul>
          <li>
            We do not use Google user data to develop, improve or train artificial intelligence (AI) or machine
            learning (ML) models, ours or anyone else&rsquo;s.
          </li>
          <li>We do not use Google user data for advertising of any kind, including targeted ads.</li>
          <li>We do not sell Google user data.</li>
          <li>We do not use it to assess credit or for lending.</li>
          <li>
            No person at ShipTiffin reads it unless you ask us to, it is needed for security (for example, to
            investigate abuse), or the law requires it.
          </li>
        </ul>
      </>
    ),
  },
  {
    id: "use",
    title: "How we use it",
    body: (
      <ul>
        <li>To run your box and the dashboard, and to sign you in.</li>
        <li>To keep the service secure: spotting abuse, scanners and break-in attempts.</li>
        <li>To tell you about your box: problems, planned maintenance, and changes to these policies.</li>
        <li>To answer you when you write to us.</li>
        <li>To bill you, once pricing exists.</li>
      </ul>
    ),
  },
  {
    id: "never",
    title: "What we never do",
    body: (
      <ul>
        <li>Sell your data, or the data of the people who use your apps.</li>
        <li>Show ads, or put advertising or cross-site tracking scripts on our pages or yours.</li>
        <li>Use your data, or your apps&rsquo; data, to train AI models.</li>
        <li>
          Use Google user data for anything but signing you in. See <a href="#google">Google user data</a>.
        </li>
      </ul>
    ),
  },
  {
    id: "providers",
    title: "Who helps us run it",
    body: (
      <>
        <p>
          A few companies process data for us to provide the service. They get only what their part needs.
        </p>
        <table className="table">
          <thead>
            <tr>
              <th scope="col">Provider</th>
              <th scope="col">What it does for us</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td>Hetzner</td>
              <td>The servers your box and this website run on, in Hetzner&rsquo;s data centres.</td>
            </tr>
            <tr>
              <td>SendGrid (Twilio)</td>
              <td>
                Delivers the email ShipTiffin itself sends: account and sign-in emails, notices about your box.
                Mail your apps send goes through the mail provider you connect, not through us.
              </td>
            </tr>
            <tr>
              <td>Cloudflare</td>
              <td>
                DNS for shiptiffin.com and shiptiffin.app, and forwarding of mail sent to our shiptiffin.com
                addresses to our inbox. Traffic to your box does not pass through its proxy.
              </td>
            </tr>
            <tr>
              <td>Google</td>
              <td>Sign in with Google, only if you choose it. See <a href="#google">Google user data</a>.</td>
            </tr>
          </tbody>
        </table>
        <p style={{ marginTop: 16 }}>
          HTTPS certificates come from Let&rsquo;s Encrypt; like every public certificate, they publish the domain
          names they cover. If you connect GitHub to deploy your code, GitHub shares the repositories you choose
          with your box. When we add a payment provider, we will list it here first.
        </p>
      </>
    ),
  },
  {
    id: "cookies",
    title: "Cookies",
    body: (
      <>
        <p>
          The dashboard sets one cookie when you sign in: a session cookie that keeps you signed in for up to 12
          hours. It is HttpOnly, Secure and SameSite=Strict, so scripts cannot read it and other sites cannot use
          it. This website sets no cookies at all.
        </p>
        <p>We use no advertising cookies, trackers or third-party analytics.</p>
        <p>
          Apps that customers host on ShipTiffin may set their own cookies. Those are the customer&rsquo;s, under
          the customer&rsquo;s own policy.
        </p>
      </>
    ),
  },
  {
    id: "retention",
    title: "How long we keep it",
    body: (
      <ul>
        <li>Your account: while it is open.</li>
        <li>App and request logs: 30 days. Measurements of the box: 30 days. Request traces: 3 days.</li>
        <li>
          Mail your apps send through your mail provider: the full message for 7 days, a log entry for 30 days.
        </li>
        <li>
          Visit counts: 365 days by default (you can change it per project). They hold no IP addresses and no
          cookies; the daily salt behind them is deleted after 48 hours.
        </li>
        <li>Data in your apps: until you delete it, or until your box is closed.</li>
        <li>Deleted databases and buckets: kept for 7 days in case you change your mind, then gone.</li>
        <li>Backup copies kept off the box: 30 days, each.</li>
        <li>
          When your box is closed, we delete the server and its disks, and with them everything on it. Backup
          copies kept off the box expire within 30 days after that. Export anything you want to keep first.
        </li>
        <li>Email you send us: as long as we need it to help you, and no longer than we have to.</li>
      </ul>
    ),
  },
  {
    id: "rights",
    title: "Your choices",
    body: (
      <>
        <p>
          You can see, correct, export or delete your data. Most of it you can handle yourself in the dashboard:
          projects export to a file, and deleting a project deletes its data. For anything else, including deleting
          your account and everything we hold about you, email {mail} from the address on your account. We reply
          within 30 days, usually much sooner.
        </p>
        <p>
          If you are someone who uses an app hosted on ShipTiffin, the app&rsquo;s owner decides what happens to
          your data in it. Ask them first; if you can&rsquo;t reach them, write to us and we will pass your request
          on.
        </p>
        <p>
          Depending on where you live, you may have further rights under data protection law, including to
          complain to your local data protection authority. We will help you use them.
        </p>
      </>
    ),
  },
  {
    id: "security",
    title: "Security",
    body: (
      <>
        <p>
          Every box is its own server, so one customer&rsquo;s apps never share a machine with another&rsquo;s.
          Traffic is HTTPS only. Databases and other services are not reachable from the internet. Secrets and
          the sign-in tokens that Google and other providers return are encrypted on the box, API keys are stored
          only as hashes, and the dashboard signs people in with one-time links or passkeys rather than passwords.
          Rate limits and automatic bans guard against scanners and brute-force attempts.
        </p>
        <p>
          No system is perfectly secure. If we learn of a breach that affects your data, we will tell you promptly.
          If you find a security problem, please write to {mail}.
        </p>
      </>
    ),
  },
  {
    id: "children",
    title: "Children",
    body: (
      <p>
        ShipTiffin is a tool for building and running software, not a service for children. You must be at least
        16 to open an account. If you believe a child has given us personal data, write to us and we will delete it.
      </p>
    ),
  },
  {
    id: "changes",
    title: "Changes to this policy",
    body: (
      <p>
        When we change this policy, we update the date at the top. If a change matters, for example a new provider
        or a new use of your data, we email account holders before it takes effect. We will never start using
        Google user data in a new way without asking you first.
      </p>
    ),
  },
  {
    id: "contact",
    title: "Contact",
    body: (
      <p>
        Email {mail} about anything in this policy. Our <Link href="/terms">terms of service</Link> cover the rest
        of how ShipTiffin works.
      </p>
    ),
  },
];

export default function Privacy() {
  return (
    <LegalPage
      title="Privacy policy"
      intro="What we collect, why, how we handle Google user data, who helps us run the service, and how to have it deleted. In plain words, because you should be able to read it."
      sections={sections}
    />
  );
}
