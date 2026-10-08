import type { Metadata } from "next";
import Link from "next/link";
import { EMAIL } from "../chrome";
import { LegalPage, type Section } from "../legal";

export const dynamic = "force-static";

export const metadata: Metadata = {
  title: "Terms of service",
  description: "The rules for using ShipTiffin, in plain English: what you can run, what you own, and what we promise.",
  alternates: { canonical: "/terms" },
};

const mail = <a href={`mailto:${EMAIL}`}>{EMAIL}</a>;

const sections: Section[] = [
  {
    id: "agreement",
    title: "The agreement",
    body: (
      <>
        <p>
          These terms are between you and ShipTiffin, the operator of the hosted Tiffin service at shiptiffin.com,
          dashboard.shiptiffin.com and shiptiffin.app (&ldquo;the service&rdquo;). By using the service, you agree
          to them. If you use it for an organisation, you agree for that organisation and confirm you may.
        </p>
        <p>
          You must be at least 16, and give us a real email address. You are responsible for what happens under
          your account, including what people you invite and the API keys and agents you create do on your box.
        </p>
      </>
    ),
  },
  {
    id: "service",
    title: "The service",
    body: (
      <>
        <p>
          We run a server (&ldquo;your box&rdquo;) for you with Tiffin installed, and keep it updated. You use it to
          run your own projects: apps, databases, files, email, jobs and the rest.
        </p>
        <p>
          ShipTiffin is in early access and Tiffin is before version 1.0. Features may change, and we may add,
          change or remove parts of the service. When a change takes something away that you rely on, we tell you
          ahead of time where we can.
        </p>
      </>
    ),
  },
  {
    id: "yours",
    title: "Your data and apps are yours",
    body: (
      <>
        <p>
          You own your code, your apps and the data in them. We claim no rights to them beyond what we need to run
          the service for you: storing them, running them, backing them up on your box and serving them to your
          visitors.
        </p>
        <p>
          You are responsible for your apps and their content, for having the rights to what you put on your box,
          and for treating your own users&rsquo; data lawfully, including telling them how you use it.
        </p>
        <p>
          We keep restore points on your box and backup copies off it for 30 days. No backup is a guarantee, so for
          anything that matters, keep your own copies too: export your projects regularly.
        </p>
      </>
    ),
  },
  {
    id: "use",
    title: "Acceptable use",
    body: (
      <>
        <p>Don&rsquo;t use ShipTiffin to:</p>
        <ul>
          <li>send spam or unsolicited bulk email, or mail to lists of people who did not ask for it;</li>
          <li>host or spread malware, phishing pages, or anything built to deceive or steal;</li>
          <li>attack, scan, probe or overload other systems, or get around their security;</li>
          <li>host content that is illegal, that exploits children, or that infringes other people&rsquo;s rights;</li>
          <li>
            create, host or share intimate or sexual images or videos of a real person without their consent,
            including ones made or altered with AI (non-consensual intimate imagery, or NCII), or any sexual
            content involving people who have not consented;
          </li>
          <li>harass, threaten or abuse people;</li>
          <li>mine cryptocurrency, or resell the service, without our written agreement;</li>
          <li>get around the limits of your box or account, or interfere with how the service runs.</li>
        </ul>
        <p>
          If your app uses ShipTiffin&rsquo;s shared Sign in with Google, use the information it gives you only to
          sign people in and to provide your app to them, follow the{" "}
          <a href="https://developers.google.com/terms/api-services-user-data-policy">
            Google API Services User Data Policy
          </a>{" "}
          (including its Limited Use requirements), and never sell that data, use it for advertising, use it to
          train AI models, or use it, or any Google API, to create non-consensual intimate imagery.
        </p>
        <h3>Email</h3>
        <p>
          Your apps send mail through the mail provider you connect to your box, under that provider&rsquo;s
          terms as well as these. Abusive mail from a box still harms our servers&rsquo; reputation and other
          customers. If your box sends spam, phishing or other abusive mail, we may stop its mail or suspend the
          box right away, and tell you why.
        </p>
      </>
    ),
  },
  {
    id: "suspension",
    title: "Suspension",
    body: (
      <p>
        We may suspend a box or account that breaks these terms, puts the service or other people at risk, or that
        we are legally required to stop. Where we can, we warn you first and give you a chance to fix the problem.
        When it can&rsquo;t wait (abusive mail, malware, non-consensual intimate imagery, an active attack), we
        act first and tell you straight after. A suspended box keeps its data while we sort it out.
      </p>
    ),
  },
  {
    id: "payment",
    title: "Payment",
    body: (
      <>
        <p>
          Pricing isn&rsquo;t set yet. During early access, we will tell you the price of your box before you
          are ever charged, and you won&rsquo;t be charged without agreeing to it.
        </p>
        <p>
          Once pricing exists, the price and how billing works will be shown before you subscribe, and these terms
          will be updated to cover them. Prices are for a box, flat, not for usage.
        </p>
      </>
    ),
  },
  {
    id: "leaving",
    title: "Leaving, and taking your data",
    body: (
      <>
        <p>
          You can stop using ShipTiffin at any time. Before you go, export your projects: each one exports to a
          single <code>.tiffin</code> file with its code, database, files and settings, which imports into any
          Tiffin box, and whose contents are ordinary files you can use without Tiffin.
        </p>
        <p>
          When you close your account, or we end the service for you, we give you at least 30 days to export,
          except when we suspended you for abuse that makes that unsafe. After that we delete your box and
          everything on it.
        </p>
        <p>
          We may end these terms with 30 days&rsquo; notice for any reason, or straight away for a serious breach.
          If we ever shut the service down, we will give you at least 60 days&rsquo; notice.
        </p>
      </>
    ),
  },
  {
    id: "warranty",
    title: "No warranty",
    body: (
      <p>
        We work hard to keep your box running, secure and backed up. But the service is provided{" "}
        <strong>&ldquo;as is&rdquo; and &ldquo;as available&rdquo;</strong>, without warranties of any kind, express
        or implied, including that it will be uninterrupted, error-free or fit for a particular purpose. Each box is
        one server: if it goes down, your apps go down with it. Don&rsquo;t rely on ShipTiffin alone for anything
        where downtime or data loss would cause serious harm.
      </p>
    ),
  },
  {
    id: "liability",
    title: "Limitation of liability",
    body: (
      <>
        <p>
          To the fullest extent the law allows, ShipTiffin is not liable for any indirect, incidental, special,
          consequential or punitive damages, or for lost profits, revenue, data or goodwill, arising from your use
          of the service, even if we were told they were possible.
        </p>
        <p>
          Our total liability for any claim about the service is limited to the amount you paid us for it in the
          12 months before the claim, or, if you paid nothing, to the equivalent of 50 US dollars.
        </p>
        <p>
          Some places don&rsquo;t allow some of these limits. Where that&rsquo;s so, they apply only as far as the
          law permits, and nothing here limits rights you have that can&rsquo;t be given up by contract.
        </p>
      </>
    ),
  },
  {
    id: "indemnity",
    title: "Your responsibility for your apps",
    body: (
      <p>
        If someone makes a claim against ShipTiffin because of your apps, your content or your breach of these
        terms, you agree to cover the reasonable costs that claim causes us, as far as the law allows.
      </p>
    ),
  },
  {
    id: "privacy",
    title: "Privacy",
    body: (
      <p>
        Our <Link href="/privacy">privacy policy</Link> explains what we collect and why. It is part of these terms.
      </p>
    ),
  },
  {
    id: "changes",
    title: "Changes to these terms",
    body: (
      <p>
        We may update these terms. When we do, we change the date at the top, and for changes that matter we email
        account holders at least 14 days before they take effect. If you keep using the service after that, the new
        terms apply. If you don&rsquo;t agree, you can export your projects and leave.
      </p>
    ),
  },
  {
    id: "contact",
    title: "Contact",
    body: (
      <p>
        Questions, notices and reports of abuse go to {mail}. If a part of these terms turns out to be
        unenforceable, the rest still applies. Not enforcing a part right away doesn&rsquo;t mean we give it up.
      </p>
    ),
  },
];

export default function Terms() {
  return (
    <LegalPage
      title="Terms of service"
      intro="The rules for using ShipTiffin: what you can run, what stays yours, and what we can and can't promise."
      sections={sections}
    />
  );
}
