import { Link, Text } from "react-email";
import { Box, boxSample, boxVars } from "../ui/box";
import { defineEmail, getMode, v, when, whenInline, type PropsOf } from "../ui/compile";
import { Cta, Facts, H1, P } from "../ui/parts";

// Sent to the person who created an API key in the dashboard. A key outlives
// the session it was made in, so a stolen session's key must not go
// unnoticed: what the key can do, until when, and where it was made from,
// then the one place to revoke it. Same shape as the new sign-in notice.
export const spec = defineEmail({
  id: "new-key",
  family: "box",
  title: "Someone created an API key in the dashboard (sent to them)",
  vars: {
    ...boxVars,
    first: v.optText("First name; empty when the box doesn't know it"),
    by: v.text('Who created it: "Sam Rivera"'),
    name: v.text('The key\'s name: "ci"'),
    access: v.text('What it can do, in words: "Full access to all projects (admin)", "Read only: shop, blog"'),
    expires: v.text('"Never", "6 January 2027"'),
    when: v.text('"Wednesday 7 October, 14:32 UTC"'),
    device: v.optText('The browser it was made in: "Chrome on macOS"; empty when unknown'),
    where: v.optText('The country the address is in, "United States"; empty when unknown'),
    ip: v.optText("The address it came from; empty to leave it out"),
    url: v.optUrl("The dashboard's API keys page; empty for no button"),
    shownUrl: v.text('The same address without https://: "dashboard.example.com/settings/keys"'),
    signInsUrl: v.optUrl("Their sign-ins page (to sign out everywhere else); empty for none"),
  },
  subject: (p) => `New API key "${p.name}" on ${p.brand}`,
  preview: {
    ...boxSample,
    first: "Sam",
    by: "Sam Rivera",
    name: "ci",
    access: "Full access to all projects (admin)",
    expires: "Never",
    when: "Wednesday 7 October, 14:32 UTC",
    device: "Chrome on macOS",
    where: "United Kingdom",
    ip: "198.51.100.7",
    url: "https://dashboard.shiptiffin.com/settings/keys",
    shownUrl: "dashboard.shiptiffin.com/settings/keys",
    signInsUrl: "https://dashboard.shiptiffin.com/settings/sign-ins",
  },
});

const muted = "tf-ink3 text-ink-3 text-[13px]";
const link = "tf-link text-link underline";

export default function NewKey(p: PropsOf<typeof spec.vars>) {
  const text = getMode() === "text";
  return (
    <Box p={p} preview={`${p.access}. Expires: ${p.expires}. If you made it, there's nothing to do.`} why="Sent for every API key made in the dashboard" oneLine>
      <H1 serif>New API key</H1>
      <P>
        {whenInline(p, "first", <>{p.first}, an</>, <>An</>)} API key was just created on your box from your dashboard sign-in. It works until it
        expires or you revoke it, even after you sign out.
      </P>
      <Facts
        narrow
        rows={[
          { label: "Key", value: p.name },
          { label: "Access", value: p.access },
          { label: "Expires", value: p.expires },
          { label: "By", value: p.by },
          { label: "When", value: p.when },
          { label: "Device", value: p.device, wrap: (row) => when(p, "device", row) },
          {
            label: "Where",
            value: whenInline(
              p,
              "where",
              <>
                {p.where}
                <span className={muted}>
                  {" · "}
                  {p.ip}
                </span>
              </>,
              <span className={muted}>{p.ip}</span>,
            ),
            wrap: (row) => when(p, "ip", row),
          },
        ]}
      />
      <P>If you made it, there's nothing to do.</P>
      {when(
        p,
        "url",
        <>
          <Cta href={p.url} tight>
            Review API keys
          </Cta>
          {text ? null : (
            <Text className={`${muted} m-0 mt-[10px] mb-[22px] font-sans leading-[20px]`}>
              Or open{" "}
              <Link href={p.url} className="tf-ink2 text-ink-2 no-underline [word-break:break-word]">
                {p.shownUrl}
              </Link>
            </Text>
          )}
        </>,
      )}
      <P quiet tight>
        <b className="tf-ink text-ink font-semibold">Wasn't you?</b> Revoke the key on {whenInline(p, "url", <>that page</>, <>the dashboard's API keys page</>)}, then{" "}
        {whenInline(
          p,
          "signInsUrl",
          text ? (
            <>sign out everywhere else: {p.signInsUrl}</>
          ) : (
            <>
              <Link href={p.signInsUrl} className={link}>
                sign out everywhere else
              </Link>
              .
            </>
          ),
          <>sign out everywhere else on your sign-ins page.</>,
        )}
      </P>
    </Box>
  );
}
NewKey.PreviewProps = spec.preview;
