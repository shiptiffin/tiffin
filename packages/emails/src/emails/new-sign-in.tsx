import { Link, Text } from "react-email";
import { Box, boxSample, boxVars } from "../ui/box";
import { defineEmail, getMode, v, when, whenInline, type PropsOf } from "../ui/compile";
import { Cta, Facts, H1, P } from "../ui/parts";

// A notice, not an alarm: what happened in one line, the facts, and what to
// do if it wasn't them (sign out everywhere else on their sign-ins page, then
// passkeys and API keys). The box is named once (the brand line); no address
// is written out as bare text, so mail apps don't turn it into a raw link.
export const spec = defineEmail({
  id: "new-sign-in",
  family: "box",
  title: "Someone signed in from a browser the box hasn't seen them use",
  vars: {
    ...boxVars,
    first: v.optText("First name; empty when the box doesn't know it"),
    device: v.text('"Chrome on macOS"'),
    from: v.text('The device inside a sentence, for the subject: "Chrome on macOS", "an unknown browser"'),
    when: v.text('"Wednesday 7 October, 14:32 UTC"'),
    how: v.text('"Google", "GitHub", "Passkey", "Sign-in link"'),
    where: v.optText('The country the address is in, "United States"; empty when unknown'),
    ip: v.optText("The address it came from; empty to leave it out"),
    admin: v.flag("An owner or admin: they can revoke API keys themselves"),
    url: v.optUrl("Their sign-ins page in the dashboard (where they can sign out everywhere else); empty for no button"),
    shownUrl: v.text('The same address without https://, to show under the button: "dashboard.example.com/settings/sign-ins"'),
    passkeysUrl: v.optUrl("Their passkeys page in the dashboard; empty for none"),
    keysUrl: v.optUrl("The dashboard's API keys page (owners and admins); empty for none"),
  },
  subject: (p) => `New sign-in to ${p.brand} from ${p.from}`,
  preview: {
    ...boxSample,
    first: "Sam",
    device: "Safari on iPhone",
    from: "Safari on iPhone",
    when: "Wednesday 7 October, 14:32 UTC",
    how: "Passkey",
    where: "United Kingdom",
    ip: "198.51.100.7",
    admin: true,
    url: "https://dashboard.shiptiffin.com/settings/sign-ins",
    shownUrl: "dashboard.shiptiffin.com/settings/sign-ins",
    passkeysUrl: "https://dashboard.shiptiffin.com/settings/passkeys",
    keysUrl: "https://dashboard.shiptiffin.com/settings/keys",
  },
});

const muted = "tf-ink3 text-ink-3 text-[13px]";
const link = "tf-link text-link underline";

export default function NewSignIn(p: PropsOf<typeof spec.vars>) {
  const text = getMode() === "text";
  return (
    <Box p={p} preview={`${p.when}. If this was you, there's nothing to do.`} why="Sent once per new browser" oneLine>
      <H1 serif>New sign-in</H1>
      <P>
        {whenInline(p, "first", <>{p.first}, there's</>, <>There's</>)} a new sign-in to your box from a browser it hasn't seen before.
      </P>
      <Facts
        narrow
        rows={[
          { label: "Device", value: p.device },
          { label: "When", value: p.when },
          { label: "How", value: p.how },
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
      <P>If this was you, there's nothing to do.</P>
      {when(
        p,
        "url",
        <>
          <Cta href={p.url} tight>
            Review sign-ins
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
        <b className="tf-ink text-ink font-semibold">Wasn't you?</b> Choose{" "}
        {whenInline(
          p,
          "url",
          text ? (
            <>Sign out everywhere else on that page</>
          ) : (
            <Link href={p.url} className={link}>
              Sign out everywhere else
            </Link>
          ),
          <>Sign out everywhere else in the dashboard</>,
        )}
        , then remove any{" "}
        {whenInline(
          p,
          "passkeysUrl",
          text ? (
            <>passkey you don't recognise ({p.passkeysUrl})</>
          ) : (
            <Link href={p.passkeysUrl} className={link}>
              passkey you don't recognise
            </Link>
          ),
          <>passkey you don't recognise</>,
        )}
        {when(
          p,
          "admin",
          <>
            {" "}and{" "}
            {whenInline(
              p,
              "keysUrl",
              text ? (
                <>revoke API keys you didn't make: {p.keysUrl}</>
              ) : (
                <>
                  <Link href={p.keysUrl} className={link}>
                    revoke API keys you didn't make
                  </Link>
                  .
                </>
              ),
              <>revoke API keys you didn't make.</>,
            )}
          </>,
          <>, and tell the box's owner.</>,
          { inline: true },
        )}
      </P>
    </Box>
  );
}
NewSignIn.PreviewProps = spec.preview;
