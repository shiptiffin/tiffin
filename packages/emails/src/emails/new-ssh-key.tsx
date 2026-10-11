import { Link, Text } from "react-email";
import { Box, boxSample, boxVars } from "../ui/box";
import { choose, defineEmail, getMode, v, when, whenInline, type PropsOf } from "../ui/compile";
import { Cta, Facts, H1, P } from "../ui/parts";

// Sent to the box's owner when SSH keys are added to the server (Settings ›
// Server access). A key there logs in as root, so one added from a stolen
// session must not go unnoticed: which key, where it came from, when and
// from where, then the one place to remove it. Same shape as the new
// passkey notice.
export const spec = defineEmail({
  id: "new-ssh-key",
  family: "box",
  title: "SSH keys were added to the server (sent to the owner)",
  vars: {
    ...boxVars,
    first: v.optText("First name; empty when the box doesn't know it"),
    count: v.oneOf(["one", "several"], "How many keys were added"),
    server: v.text('The server they log in to: "new-box.shiptiffin.app"'),
    keys: v.text('The keys\' fingerprints: "SHA256:3fGk… (ssh-ed25519)", several joined with ", "'),
    source: v.text('Where they came from: "Pasted", "GitHub (@sam)"'),
    when: v.text('"Wednesday 7 October, 14:32 UTC"'),
    device: v.optText('Where they were added: "Chrome on macOS", "The tiffin CLI (owner token)"; empty when unknown'),
    where: v.optText('The country the address is in, "United States"; empty when unknown'),
    ip: v.optText("The address it came from; empty to leave it out"),
    url: v.optUrl("Server access in the dashboard; empty for no button"),
    shownUrl: v.text('The same address without https://: "dashboard.example.com/settings/server-access"'),
    signInsUrl: v.optUrl("The owner's sign-ins page (to sign out everywhere else); empty for none"),
  },
  subject: (p) => `${choose(p, "count", { one: "An SSH key was", several: "SSH keys were" })} added to ${p.server}`,
  preview: {
    ...boxSample,
    first: "Sam",
    count: "one",
    server: "new-box.shiptiffin.app",
    keys: "SHA256:3fGkVq0n8x1sQ2b7mWl4cYp9dZt6uRj5hE0aKiLoNwU (ssh-ed25519)",
    source: "GitHub (@sam)",
    when: "Wednesday 7 October, 14:32 UTC",
    device: "Chrome on macOS",
    where: "United Kingdom",
    ip: "198.51.100.7",
    url: "https://dashboard.shiptiffin.com/settings/server-access",
    shownUrl: "dashboard.shiptiffin.com/settings/server-access",
    signInsUrl: "https://dashboard.shiptiffin.com/settings/sign-ins",
  },
});

const muted = "tf-ink3 text-ink-3 text-[13px]";
const link = "tf-link text-link underline";

export default function NewSSHKey(p: PropsOf<typeof spec.vars>) {
  const text = getMode() === "text";
  return (
    <Box p={p} preview={`${p.server}, ${p.when}. If you added it, there's nothing to do.`} why="Sent for every SSH key added to your server" oneLine>
      <H1 serif>{choose(p, "count", { one: <>New SSH key</>, several: <>New SSH keys</> }, null, { inline: true })}</H1>
      <P>
        {whenInline(
          p,
          "first",
          <>
            {p.first}, {choose(p, "count", { one: <>a new SSH key</>, several: <>new SSH keys</> }, null, { inline: true })}
          </>,
          choose(p, "count", { one: <>A new SSH key</>, several: <>New SSH keys</> }, null, { inline: true }),
        )}{" "}
        can now log in to your server,{" "}
        <b className="tf-ink text-ink font-semibold">{p.server}</b>, as root.
      </P>
      <Facts
        narrow
        rows={[
          { label: "Key", value: p.keys },
          { label: "From", value: p.source },
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
            Review server access
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
        <b className="tf-ink text-ink font-semibold">Wasn't you?</b> Remove it on {whenInline(p, "url", <>that page</>, <>the dashboard's Server access page</>)}, then{" "}
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
NewSSHKey.PreviewProps = spec.preview;
