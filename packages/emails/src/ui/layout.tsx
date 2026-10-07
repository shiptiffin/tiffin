// The two layouts. Both: a full-bleed page colour, a header line, one card
// with the message, and small print under it. Single column, 560px wide
// (fixed-width table for Outlook on Windows), fluid below that.
import type { ReactNode } from "react";
import { Body, Column, Container, Head, Html, Img, Preview, Row, Section, Tailwind, Text, pixelBasedPreset } from "react-email";
import { headCss, phoneCss, themeCss } from "./brand";
import { getMode, raw, type Family } from "./compile";

function Shell({ family, preview, header, children, footer }: { family: Family; preview: string; header: ReactNode; children: ReactNode; footer: ReactNode }) {
  const text = getMode() === "text";
  return (
    <Html lang="en">
      <Tailwind config={{ presets: [pixelBasedPreset] }} theme={themeCss(family)}>
        <Head>
          <meta name="viewport" content="width=device-width, initial-scale=1" />
          <meta name="color-scheme" content="light dark" />
          <meta name="supported-color-schemes" content="light dark" />
          <meta name="format-detection" content="telephone=no, date=no, address=no, email=no, url=no" />
          <style>{headCss(family)}</style>
          <style>{phoneCss}</style>
        </Head>
        <Body className="tf-bg m-0 bg-bg p-0">
          <Preview>{preview}</Preview>
          {/* The page colour again, on a table: Body puts it on its own cell, where the dark-mode class can't reach. */}
          <Section className="tf-bg bg-bg">
          {raw("msoOpen")}
          <Container className="max-w-[560px] px-[20px] pt-[36px] pb-[40px]" tdClassName="tf-pad">
            {text ? null : <Section className="mb-[18px] px-[4px]">{header}</Section>}
            <Section className="tf-card rounded-[12px] border border-solid border-rule bg-card px-[36px] pt-[34px] pb-[30px]" tdClassName="tf-cardpad">
              {children}
            </Section>
            <Section className="mt-[20px] px-[4px]">{footer}</Section>
          </Container>
          {raw("msoClose")}
          </Section>
        </Body>
      </Tailwind>
    </Html>
  );
}

/** ShipTiffin / Tiffin: the box's own mail. */
export function BoxLayout(props: {
  preview: string;
  brand: string;
  host: string;
  /** The mark, a PNG; optional, the wordmark reads without it. */
  mark: ReactNode;
  children: ReactNode;
  why: ReactNode;
  /** Replaces the whole footer (why, then brand · host). */
  footer?: ReactNode;
}) {
  return (
    <Shell
      family="box"
      preview={props.preview}
      header={
        <Row>
          {props.mark}
          <Column className="align-middle">
            <Text className="tf-ink m-0 font-sans text-[16px] leading-[22px] font-semibold tracking-[-0.1px] text-ink">{props.brand}</Text>
          </Column>
        </Row>
      }
      footer={
        props.footer ?? (
          <>
            {props.why}
            <Text className="tf-ink3 m-0 font-sans text-[12px] leading-[18px] text-ink-3">
              {props.brand} · {props.host}
            </Text>
          </>
        )
      }
    >
      {props.children}
    </Shell>
  );
}

export function MarkCell({ src }: { src: string }) {
  return (
    <Column className="w-[38px] align-middle">
      <Img src={src} width="28" height="28" alt="" className="block rounded-[7px]" />
    </Column>
  );
}

/** A customer's app: its name, logo and accent; nothing of ours. */
export function AppLayout(props: { preview: string; app: string; logo: ReactNode; children: ReactNode; why: ReactNode; site: ReactNode }) {
  return (
    <Shell
      family="app"
      preview={props.preview}
      header={
        <Row>
          {props.logo}
          <Column className="align-middle">
            <Text className="tf-ink m-0 font-sans text-[16px] leading-[22px] font-semibold text-ink">{props.app}</Text>
          </Column>
        </Row>
      }
      footer={
        <>
          {props.why}
          {props.site}
        </>
      }
    >
      {props.children}
    </Shell>
  );
}

export function LogoCell({ src }: { src: string }) {
  return (
    <Column className="w-[42px] align-middle">
      <Img src={src} width="32" height="32" alt="" className="block rounded-[8px]" />
    </Column>
  );
}
