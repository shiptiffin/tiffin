import { Box, boxSample, boxVars } from "../ui/box";
import { choose, defineEmail, v, type PropsOf } from "../ui/compile";
import { Cta, H1, LinkOut, P } from "../ui/parts";

export const spec = defineEmail({
  id: "invite",
  family: "box",
  title: "Invite to a box: a new person's first sign-in link",
  vars: {
    ...boxVars,
    first: v.text('First name, or "there"'),
    by: v.text('Who invited them: a name, or "The box\'s owner"'),
    role: v.oneOf(["owner", "admin", "member", "viewer"], "Their role on the box"),
    url: v.url("The one-time sign-in link"),
    until: v.text('How long the link works: "for the next 30 minutes", "until 13 October 2026, 21:05 UTC"'),
  },
  subject: (p) => `You're invited to ${p.host}`,
  preview: { ...boxSample, first: "Sam", by: "Bilal Tahir", role: "member", url: "https://dashboard.shiptiffin.com/login#inv_7Hk2pQ9xW", until: "until 9 October 2026, 18:00 UTC" },
});

export default function Invite(p: PropsOf<typeof spec.vars>) {
  return (
    <Box p={p} preview={`${p.by} added you to ${p.host}. Your sign-in link is inside.`} why="If you weren't expecting this, you can ignore it. Nothing happens unless you open the link.">
      <H1 serif>You're invited</H1>
      <P>Hi {p.first},</P>
      <P>
        {p.by} added you to the {p.brand} box at {p.host}.{" "}
        {choose(p, "role", {
          owner: "You'll join as an owner: you can do everything, including changing the box itself.",
          admin: "You'll join as an admin: you can do everything except change the owner.",
          member: "You'll join as a member: you can plan and apply changes, including ones that reach outside the box.",
          viewer: "You'll join as a viewer: you can see everything and change nothing.",
        }, undefined, { inline: true })}
      </P>
      <Cta href={p.url}>Accept and sign in</Cta>
      <P quiet>
        The link works once, {p.until}. After that, ask for a new one. Once you're in, add a passkey in Settings and you won't need links again.
      </P>
      <LinkOut href={p.url} />
    </Box>
  );
}
Invite.PreviewProps = spec.preview;
