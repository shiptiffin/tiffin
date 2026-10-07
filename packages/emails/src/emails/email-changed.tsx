import { App, appSample, appVars } from "../ui/app";
import { defineEmail, v, when, type PropsOf } from "../ui/compile";
import { Eyebrow, Facts, H1, P } from "../ui/parts";
import { Link } from "react-email";

export const spec = defineEmail({
  id: "email-changed",
  family: "app",
  title: "Notice to the old address that the account's email changed",
  vars: {
    ...appVars,
    newEmail: v.text("The new address"),
    when: v.text('"7 October 2026, 14:32 UTC"'),
    support: v.optUrl("Where to get help (mailto: or https); empty to leave it out"),
    supportLabel: v.optText('How it is shown: "help@larder.app"'),
  },
  subject: (p) => `Your ${p.app} email was changed`,
  preview: { ...appSample, newEmail: "sam.lee@example.org", when: "7 October 2026, 14:32 UTC", support: "mailto:help@larder.app", supportLabel: "help@larder.app" },
});

export default function EmailChanged(p: PropsOf<typeof spec.vars>) {
  return (
    <App p={p} preview={`Your ${p.app} account now uses ${p.newEmail}.`} why={`We sent this to your old address, ${p.email}, so you'd know.`}>
      <Eyebrow>Security notice</Eyebrow>
      <H1>Your email was changed</H1>
      <P>The email for your {p.app} account was changed. From now on, sign-in links and notices go to the new address.</P>
      <Facts
        rows={[
          { label: "Old", value: p.email },
          { label: "New", value: p.newEmail },
          { label: "When", value: p.when },
        ]}
      />
      <P>If you made this change, there's nothing to do.</P>
      {when(
        p,
        "support",
        <P>
          If you didn't, contact{" "}
          <Link href={p.support} className="tf-link tf-plain text-link underline">
            {p.supportLabel}
          </Link>{" "}
          straight away so the account can be locked and returned to you.
        </P>,
        <P>If you didn't, contact the {p.app} team straight away so the account can be locked and returned to you.</P>,
      )}
    </App>
  );
}
EmailChanged.PreviewProps = spec.preview;
