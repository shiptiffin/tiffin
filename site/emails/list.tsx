// The sign-up list's two emails (lib/emails.ts): the confirmation, with a
// remove link, and the note to the owner when someone confirms.
import { A, Cta, Facts, H1, Layout, LinkOut, P, type Fact } from "./ui";

export function Confirm({ hello, links }: { hello: string; links: { confirm: string; remove: string } }) {
  return (
    <Layout
      title="Confirm your email"
      preview="One click and you're on the list."
      why={
        <>
          Didn't ask for this? Ignore this email and you won't hear from us again, or <A href={links.remove}>remove your address</A>.
        </>
      }
    >
      <H1>Confirm your email</H1>
      <P>{hello}</P>
      <P>Thanks for joining the ShipTiffin sign-up list. Confirm this is your address.</P>
      <Cta href={links.confirm}>Confirm my email</Cta>
      <P>
        Sign-up opens this week, and we'll send you the link as soon as it does. The first 100 customers pay $12 a month instead of $19, locked for
        24 months. Until then, we won't write.
      </P>
      <LinkOut href={links.confirm} />
    </Layout>
  );
}

/** For the owner: who confirmed, and what they told us. */
export function OwnerNote({ who, rows, where }: { who: string; rows: Fact[]; where: string }) {
  return (
    <Layout title="Sign-up list" preview={`${who} confirmed their place on the sign-up list.`} why="Sent to you because EARLY_ACCESS_NOTIFY names this address.">
      <H1>New on the list</H1>
      <P>
        <strong>{who}</strong> confirmed their place on the sign-up list. Reply to write to them.
      </P>
      <Facts rows={rows} labelWidth={132} />
      <P quiet>{where}</P>
    </Layout>
  );
}
