import { Link, Text } from "react-email";
import { Box, boxSample, boxVars } from "../ui/box";
import { defineEmail, getMode, v, when, whenInline, type PropsOf } from "../ui/compile";
import { Cta, Facts, H1, P } from "../ui/parts";

// Sent to the person a passkey was added for. A passkey signs in for good and
// passes every later "confirm it's you", so one added from a stolen session
// must not go unnoticed: which passkey, when, and where it was added from,
// then the one place to remove it. Same shape as the new API key notice.
export const spec = defineEmail({
  id: "new-passkey",
  family: "box",
  title: "A passkey was added to someone's sign-ins (sent to them)",
  vars: {
    ...boxVars,
    first: v.optText("First name; empty when the box doesn't know it"),
    name: v.text('The passkey\'s name: "MacBook"'),
    when: v.text('"Wednesday 7 October, 14:32 UTC"'),
    device: v.optText('The browser it was added in: "Chrome on macOS"; empty when unknown'),
    where: v.optText('The country the address is in, "United States"; empty when unknown'),
    ip: v.optText("The address it came from; empty to leave it out"),
    url: v.optUrl("Their passkeys page in the dashboard; empty for no button"),
    shownUrl: v.text('The same address without https://: "dashboard.example.com/settings/passkeys"'),
    signInsUrl: v.optUrl("Their sign-ins page (to sign out everywhere else); empty for none"),
  },
  subject: (p) => `New passkey "${p.name}" on ${p.brand}`,
  preview: {
    ...boxSample,
    first: "Sam",
    name: "MacBook",
    when: "Wednesday 7 October, 14:32 UTC",
    device: "Chrome on macOS",
    where: "United Kingdom",
    ip: "198.51.100.7",
    url: "https://dashboard.shiptiffin.com/settings/passkeys",
    shownUrl: "dashboard.shiptiffin.com/settings/passkeys",
    signInsUrl: "https://dashboard.shiptiffin.com/settings/sign-ins",
  },
});

const muted = "tf-ink3 text-ink-3 text-[13px]";
const link = "tf-link text-link underline";

export default function NewPasskey(p: PropsOf<typeof spec.vars>) {
  const text = getMode() === "text";
  return (
    <Box p={p} preview={`${p.name}, ${p.when}. If you added it, there's nothing to do.`} why="Sent for every passkey added to your sign-ins" oneLine>
      <H1 serif>New passkey</H1>
      <P>
        {whenInline(p, "first", <>{p.first}, a</>, <>A</>)} passkey was just added to your sign-ins. From now on it signs in to your box as you.
      </P>
      <Facts
        narrow
        rows={[
          { label: "Passkey", value: p.name },
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
      <P>If you added it, there's nothing to do.</P>
      {when(
        p,
        "url",
        <>
          <Cta href={p.url} tight>
            Review passkeys
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
        <b className="tf-ink text-ink font-semibold">Wasn't you?</b> Remove it on {whenInline(p, "url", <>that page</>, <>the dashboard's passkeys page</>)}, then{" "}
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
NewPasskey.PreviewProps = spec.preview;
