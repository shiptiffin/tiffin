// Building blocks shared by both families. Colours come from the family's
// Tailwind theme (brand.ts), so the same part looks right in either. tf-*
// classes are hooks for the dark-mode block; everything else is inlined.
import type { CSSProperties, ReactNode } from "react";
import { Button, Heading, Hr, Link, Section, Text } from "react-email";
import { getMode } from "./compile";

const textMode = () => getMode() === "text";

export function H1({ children, serif }: { children: ReactNode; serif?: boolean }) {
  return (
    <Heading
      as="h1"
      className={`tf-ink tf-h1 m-0 mb-[14px] p-0 text-ink ${serif ? "font-serif text-[27px] leading-[34px] font-normal tracking-[-0.2px]" : "font-sans text-[23px] leading-[30px] font-semibold tracking-[-0.3px]"}`}
    >
      {children}
    </Heading>
  );
}

/** A small status word above the heading: "Firing", "Resolved", "Security". */
export function Eyebrow({ children, tone = "quiet" }: { children: ReactNode; tone?: "quiet" | "danger" | "ok" }) {
  const c = tone === "danger" ? "tf-danger text-danger" : tone === "ok" ? "tf-ok text-ok" : "tf-ink3 text-ink-3";
  return <Text className={`${c} m-0 mb-[8px] font-sans text-[13px] leading-[18px] font-semibold tracking-[0.2px]`}>{children}</Text>;
}

export function P({ children, quiet, tight }: { children: ReactNode; quiet?: boolean; tight?: boolean }) {
  return (
    <Text
      className={`${quiet ? "tf-ink2 text-ink-2 text-[14px] leading-[22px]" : "tf-ink text-ink text-[15px] leading-[24px]"} m-0 ${tight ? "mb-[6px]" : "mb-[14px]"} font-sans`}
    >
      {children}
    </Text>
  );
}

/**
 * The one action: a bulletproof button (React Email's Button pads Outlook with
 * mso spacers). It is the dashboard's primary button (components/ui/button.tsx):
 * brass, 8px corners, semibold, the same top light and drop shadow where the
 * mail client draws shadows; a little taller, for thumbs.
 */
const ctaShadow = "inset 0 1px 0 rgba(255,255,255,0.2),0 1px 1px rgba(62,41,15,0.2)"; // oklch(1 0 0/.2), oklch(.3 .05 70/.2)

/** `tight`: no space under the button, for a line that belongs to it (the link written out). */
export function Cta({ href, children, style, tight }: { href: string; children: ReactNode; style?: CSSProperties; tight?: boolean }) {
  if (textMode()) {
    return (
      <p>
        {children}: <a href={href}>{href}</a>
      </p>
    );
  }
  return (
    <Section className={`mt-[22px] ${tight ? "mb-0" : "mb-[22px]"}`}>
      <Button
        href={href}
        className="tf-btn box-border rounded-[8px] bg-accent px-[22px] py-[13px] font-sans text-[15px] leading-[20px] font-semibold text-on-accent no-underline"
        style={{ boxShadow: ctaShadow, ...style }}
      >
        {children}
      </Button>
    </Section>
  );
}

/** The link under the button, written out, for when the button can't be clicked. */
export function LinkOut({ href }: { href: string }) {
  if (textMode()) return null;
  return (
    <Text className="tf-ink3 m-0 mt-[18px] font-sans text-[13px] leading-[20px] text-ink-3">
      If the button doesn't work, copy this link into your browser:
      <br />
      <Link href={href} className="tf-link break-all text-link underline">
        {href}
      </Link>
    </Text>
  );
}

/** A code to type in: big, spaced, easy to read aloud. */
export function Code({ children }: { children: ReactNode }) {
  if (textMode()) return <p>{children}</p>;
  return (
    <Section className="mt-[6px] mb-[22px]">
      <Text
        className="tf-well m-0 inline-block rounded-[10px] border border-solid border-rule bg-well py-[14px] pr-[18px] pl-[24px] font-mono text-[32px] leading-[38px] font-semibold tracking-[6px] text-ink"
      >
        {children}
      </Text>
    </Section>
  );
}

export type Fact = { label: string; value: ReactNode; wrap?: (row: ReactNode) => ReactNode };

/** Label / value rows: where, when, which browser. `wrap` makes a row conditional; `narrow` is for short labels. */
export function Facts({ rows, narrow }: { rows: Fact[]; narrow?: boolean }) {
  const w = (f: Fact, node: ReactNode) => (f.wrap ? f.wrap(node) : node);
  if (textMode()) {
    return (
      <div>
        {rows.map((f) => (
          <Frag key={f.label}>{w(f, <div>{`${f.label}: `}{f.value}</div>)}</Frag>
        ))}
      </div>
    );
  }
  return (
    <table role="presentation" width="100%" cellPadding={0} cellSpacing={0} border={0} className="tf-rule mt-[4px] mb-[18px] border-0 border-t border-solid border-rule">
      <tbody>
        {rows.map((f) => (
          <Frag key={f.label}>
            {w(
              f,
              <tr>
                <td className={`tf-ink3 tf-rule ${narrow ? "w-[72px]" : "w-[96px]"} border-0 border-b border-solid border-rule py-[9px] pr-[12px] align-top font-sans text-[13px] leading-[20px] text-ink-3`}>
                  {f.label}
                </td>
                <td className="tf-ink tf-rule border-0 border-b border-solid border-rule py-[9px] align-top font-sans text-[14px] leading-[20px] [word-break:break-word] text-ink">
                  {f.value}
                </td>
              </tr>,
            )}
          </Frag>
        ))}
      </tbody>
    </table>
  );
}
const Frag = ({ children }: { children?: ReactNode }) => <>{children}</>;

export function Rule() {
  if (textMode()) return null;
  return <Hr className="tf-rule my-[22px] border-0 border-t border-solid border-rule" />;
}

/** Footer lines under the card. */
export function Small({ children, last }: { children: ReactNode; last?: boolean }) {
  return <Text className={`tf-ink3 m-0 ${last ? "" : "mb-[8px]"} font-sans text-[12px] leading-[18px] text-ink-3`}>{children}</Text>;
}
