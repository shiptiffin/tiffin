import { Box, boxSample, boxVars } from "../ui/box";
import { defineEmail, v, type PropsOf } from "../ui/compile";
import { Facts, H1, P } from "../ui/parts";

export const spec = defineEmail({
  id: "relay-test",
  family: "box",
  title: "The test email from Settings › Email (tiffin email relay test)",
  vars: {
    ...boxVars,
    relay: v.text('The mail service: "Resend", "Postmark", "smtp.example.com"'),
    from: v.text("The sender it went out as"),
    when: v.text('"7 October 2026, 14:32 UTC"'),
  },
  subject: (p) => `Test email from ${p.host}`,
  preview: { ...boxSample, relay: "Resend", from: "ShipTiffin <hello@shiptiffin.com>", when: "7 October 2026, 14:32 UTC" },
});

export default function RelayTest(p: PropsOf<typeof spec.vars>) {
  return (
    <Box p={p} preview={`Mail from ${p.host} reaches real inboxes through ${p.relay}.`} why={`Someone with owner access to ${p.host} sent this test. There's nothing to do.`}>
      <H1 serif>Your mail service works</H1>
      <P>
        This is a test from the {p.brand} box at {p.host}. It reached you through {p.relay}, so sign-in links, alerts and your apps' email can reach real
        inboxes.
      </P>
      <Facts
        rows={[
          { label: "Sent through", value: p.relay },
          { label: "From", value: p.from },
          { label: "At", value: p.when },
        ]}
      />
      <P quiet>If it landed in spam, mark it as not spam once, and check the sending domain's SPF, DKIM and DMARC records in Settings › Email.</P>
    </Box>
  );
}
RelayTest.PreviewProps = spec.preview;
