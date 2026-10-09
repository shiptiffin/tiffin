// The one layout and the few parts every ShipTiffin email is made of: the
// mark and wordmark, one card with the message, small print under it. Built
// with React Email's components, styles inline (mail apps drop most <style>),
// a 600px column (a fixed-width table for Outlook on Windows), tf-* classes
// as hooks for dark mode. The same look as the box's own emails
// (packages/emails/src/ui).
//
// The plain-text part is the same render without markup (render.ts): parts
// that make no sense as text carry data-skip-in-text, tables of facts read
// as aligned columns, the button becomes "Label: link". No hooks or context
// here: these render inside React Server Components.
import type { CSSProperties, ReactNode } from "react";
import { Body, Button, Column, Container, Head, Heading, Hr, Html, Img, Link, Preview, Row, Section, Text } from "react-email";
import { darkCss, fontFaces, light as c, MARK, MONO, phoneCss, SANS, SERIF } from "./theme";

/** Snippets React can't write (conditional comments), pasted in after rendering (render.ts). */
export const MSO = {
  head: `<!--[if mso]><style>body,table,td,p,a,span,h1,h2{font-family:Arial,Helvetica,sans-serif !important}h1,.tf-serif{font-family:Georgia,'Times New Roman',serif !important}</style><![endif]-->`,
  open: `<!--[if mso]><table role="presentation" align="center" width="600" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`,
  close: `<!--[if mso]></td></tr></table><![endif]-->`,
} as const;
export const marker = (k: keyof typeof MSO) => `%%mso:${k}%%`;

const SITE_LABEL = "shiptiffin.com";
export const HELLO = "hello@shiptiffin.com";

export function Layout({ title, preview, why, children }: { title: string; preview: string; why?: ReactNode; children: ReactNode }) {
  return (
    <Html lang="en" dir="ltr">
      <Head>
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <meta name="color-scheme" content="light dark" />
        <meta name="supported-color-schemes" content="light dark" />
        <meta name="format-detection" content="telephone=no, date=no, address=no, email=no, url=no" />
        <title>{title}</title>
        <style>{fontFaces}</style>
        <style>{darkCss()}</style>
        <style>{phoneCss}</style>
        {marker("head")}
      </Head>
      <Body className="tf-bg" style={{ margin: 0, padding: 0, backgroundColor: c.bg, WebkitTextSizeAdjust: "100%" }}>
        <Preview useTitleTag={false}>{preview}</Preview>
        {/* The page colour again, on a table: Body puts it on its own cell, where the dark-mode class can't reach. */}
        <Section className="tf-bg" style={{ backgroundColor: c.bg }}>
          {marker("open")}
          <Container style={{ maxWidth: "600px", padding: "40px 20px 44px" }} tdClassName="tf-pad">
              <Section style={{ marginBottom: "20px", padding: "0 4px" }} data-skip-in-text="true">
                <Row>
                  <Column style={{ width: "38px", verticalAlign: "middle" }}>
                    <Img src={MARK} width="28" height="28" alt="" style={{ display: "block", borderRadius: "7px" }} />
                  </Column>
                  <Column style={{ verticalAlign: "middle" }}>
                    <Text className="tf-ink tf-serif" style={{ margin: 0, fontFamily: SERIF, fontSize: "20px", lineHeight: "24px", fontWeight: 500, letterSpacing: "-0.4px", color: c.ink }}>
                      ShipTiffin
                    </Text>
                  </Column>
                </Row>
              </Section>
            <Section
              className="tf-card"
              style={{ backgroundColor: c.card, border: `1px solid ${c.rule}`, borderRadius: "12px", padding: "36px 40px 34px" }}
              tdClassName="tf-cardpad"
            >
              {children}
            </Section>
            <Section style={{ marginTop: "22px", padding: "0 4px" }}>
              {why ? <Small>{why}</Small> : null}
              <Small last>
                ShipTiffin{" · "}
                <Link href="https://shiptiffin.com" className="tf-ink3 tf-plain" style={{ color: c.ink3, textDecoration: "underline" }}>
                  {SITE_LABEL}
                </Link>
                {" · "}
                <Link href={`mailto:${HELLO}`} className="tf-ink3 tf-plain" style={{ color: c.ink3, textDecoration: "underline" }}>
                  {HELLO}
                </Link>
              </Small>
            </Section>
          </Container>
          {marker("close")}
        </Section>
      </Body>
    </Html>
  );
}

const sans: CSSProperties = { fontFamily: SANS };

/** The heading. `hero`: bigger, for the one email worth celebrating. */
export function H1({ children, hero }: { children: ReactNode; hero?: boolean }) {
  return (
    <Heading
      as="h1"
      className={`tf-ink ${hero ? "tf-hero" : "tf-h1"}`}
      style={{
        margin: "0 0 14px",
        padding: 0,
        fontFamily: SERIF,
        fontWeight: 480,
        fontSize: hero ? "34px" : "29px",
        lineHeight: hero ? "40px" : "36px",
        letterSpacing: hero ? "-0.6px" : "-0.4px",
        color: c.ink,
      }}
    >
      {children}
    </Heading>
  );
}

/** A small status word above the heading: "Ready", "Billing", "Action needed". */
export function Eyebrow({ children, tone = "quiet" }: { children: ReactNode; tone?: "quiet" | "danger" | "ok" | "accent" }) {
  const [cls, color] =
    tone === "danger" ? ["tf-danger", c.danger] : tone === "ok" ? ["tf-ok", c.ok] : tone === "accent" ? ["tf-link", c.link] : ["tf-ink3", c.ink3];
  return (
    <Text className={cls} style={{ ...sans, margin: "0 0 10px", fontSize: "12px", lineHeight: "16px", fontWeight: 600, letterSpacing: "1.1px", textTransform: "uppercase", color }}>
      {children}
    </Text>
  );
}

export function P({ children, quiet, tight }: { children: ReactNode; quiet?: boolean; tight?: boolean }) {
  return (
    <Text
      className={quiet ? "tf-ink2" : "tf-ink"}
      style={{
        ...sans,
        margin: tight ? "0 0 6px" : "0 0 14px",
        fontSize: quiet ? "14px" : "15px",
        lineHeight: quiet ? "22px" : "24px",
        color: quiet ? c.ink2 : c.ink,
      }}
    >
      {children}
    </Text>
  );
}

/** The one action: a bulletproof button (React Email pads it for Outlook), the site's primary button. */
export function Cta({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Section style={{ margin: "24px 0 24px" }}>
      <Button
        href={href}
        className="tf-btn"
        style={{
          ...sans,
          boxSizing: "border-box",
          backgroundColor: c.accent,
          color: c.onAccent,
          borderRadius: "8px",
          padding: "14px 24px",
          fontSize: "15px",
          lineHeight: "20px",
          fontWeight: 600,
          textDecoration: "none",
          boxShadow: "inset 0 1px 0 rgba(255,255,255,0.2),0 1px 1px rgba(62,41,15,0.2)",
        }}
      >
        {children}
      </Button>
    </Section>
  );
}

/** The button's link written out, for when the button can't be clicked. */
export function LinkOut({ href }: { href: string }) {
  return (
    <Text data-skip-in-text="true" className="tf-ink3" style={{ ...sans, margin: "0 0 4px", fontSize: "13px", lineHeight: "20px", color: c.ink3 }}>
      Or copy this link into your browser:
      <br />
      <Link href={href} className="tf-link" style={{ color: c.link, textDecoration: "underline", wordBreak: "break-all" }}>
        {href}
      </Link>
    </Text>
  );
}

/** A link inside a sentence. */
export function A({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Link href={href} className="tf-link" style={{ color: c.link, textDecoration: "underline" }}>
      {children}
    </Link>
  );
}

/** An address or a name to type: monospace, never broken mid-word by accident. */
export function Code({ children }: { children: ReactNode }) {
  return <span style={{ fontFamily: MONO, fontSize: "0.92em", letterSpacing: "-0.1px" }}>{children}</span>;
}

export type Fact = { label: string; value: ReactNode };

/** Label / value rows between hairlines. */
export function Facts({ rows, labelWidth = 104 }: { rows: Fact[]; labelWidth?: number }) {
  const cell: CSSProperties = { ...sans, borderBottom: `1px solid ${c.rule}`, padding: "11px 0", verticalAlign: "top" };
  return (
    <table role="presentation" width="100%" cellPadding={0} cellSpacing={0} border={0} className="tf-rule tf-facts" style={{ borderTop: `1px solid ${c.rule}`, margin: "6px 0 22px" }}>
      <tbody>
        {rows.map((f) => (
          <tr key={f.label} className="tf-fact">
            <td className="tf-ink3 tf-rule tf-fl" style={{ ...cell, width: `${labelWidth}px`, paddingRight: "12px", fontSize: "13px", lineHeight: "21px", color: c.ink3 }}>
              {f.label}
            </td>
            <td className="tf-ink tf-rule" style={{ ...cell, fontSize: "14px", lineHeight: "21px", color: c.ink, wordBreak: "break-word" }}>
              {f.value}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** Numbered next steps: a brass numeral, a short title, one line under it. */
export function Steps({ items }: { items: { title: ReactNode; body: ReactNode }[] }) {
  return (
    <table role="presentation" width="100%" cellPadding={0} cellSpacing={0} border={0} className="tf-steps" style={{ margin: "4px 0 10px" }}>
      <tbody>
        {items.map((s, i) => (
          <tr key={i}>
            <td className="tf-stepnum" style={{ width: "34px", verticalAlign: "top", padding: "2px 0 14px" }}>
              <div
                className="tf-well"
                style={{
                  ...sans,
                  width: "24px",
                  height: "24px",
                  borderRadius: "12px",
                  backgroundColor: c.well,
                  border: `1px solid ${c.rule}`,
                  textAlign: "center",
                  fontSize: "12px",
                  lineHeight: "24px",
                  fontWeight: 600,
                }}
              >
                <span className="tf-link" style={{ color: c.link }}>
                  {i + 1}
                </span>
              </div>
            </td>
            <td style={{ verticalAlign: "top", padding: "0 0 14px" }}>
              <Text className="tf-ink tf-steptitle" data-n={i + 1} style={{ ...sans, margin: 0, fontSize: "15px", lineHeight: "24px", fontWeight: 600, color: c.ink }}>
                {s.title}
              </Text>
              <Text className="tf-ink2 tf-stepbody" style={{ ...sans, margin: 0, fontSize: "14px", lineHeight: "22px", color: c.ink2 }}>
                {s.body}
              </Text>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** A quiet panel for a short list: what we did, what stays. */
export function Panel({ title, items }: { title: string; items: ReactNode[] }) {
  return (
    <Section className="tf-well" style={{ backgroundColor: c.well, border: `1px solid ${c.rule}`, borderRadius: "10px", padding: "18px 20px 8px", margin: "8px 0 6px" }}>
      <Text className="tf-ink" style={{ ...sans, margin: "0 0 8px", fontSize: "13px", lineHeight: "20px", fontWeight: 600, color: c.ink }}>
        {title}
      </Text>
      {items.map((x, i) => (
        <Text key={i} className="tf-ink2 tf-item" style={{ ...sans, margin: "0 0 10px", fontSize: "14px", lineHeight: "21px", color: c.ink2 }}>
          <span className="tf-link" style={{ color: c.link }}>
            ✓
          </span>
          &nbsp;&nbsp;{x}
        </Text>
      ))}
    </Section>
  );
}

export function Rule() {
  return <Hr className="tf-rule" style={{ border: 0, borderTop: `1px solid ${c.rule}`, margin: "26px 0 24px" }} />;
}

/** Footer lines under the card. */
export function Small({ children, last }: { children: ReactNode; last?: boolean }) {
  return (
    <Text className="tf-ink3" style={{ ...sans, margin: last ? 0 : "0 0 8px", fontSize: "12px", lineHeight: "18px", color: c.ink3 }}>
      {children}
    </Text>
  );
}

/** A picture with words in the email around it (alt is for screen readers; nothing depends on seeing it). */
export function Picture({ src, alt, width, height }: { src: string; alt: string; width: number; height: number }) {
  return <Img src={src} width={String(width)} height={String(height)} alt={alt} style={{ display: "block", margin: "0 auto", border: 0, outline: "none" }} />;
}
