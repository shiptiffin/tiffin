/**
 * `tiffin-sdk/email`: send transactional email from an app on the box.
 *
 * ```tsx
 * import { send, render } from "tiffin-sdk/email";
 * import { WelcomeEmail } from "./emails/welcome";
 *
 * await send({ to: user.email, subject: "Welcome!", react: <WelcomeEmail name={user.name} /> });
 * await send({ to: "ops@example.com", subject: "Report", text: "All good." });
 * const html = await render(<WelcomeEmail name="Ada" />);   // HTML string
 * ```
 *
 * Mail goes to the box's own SMTP server (SMTP_URL, credentials per
 * project, set for every app). Until the box owner configures a relay, and
 * always for previews, the box captures it in the dev inbox (dashboard, or
 * `tiffin email messages list <project>`) instead of delivering it, so
 * development never emails real people.
 *
 * `react` needs `@react-email/render` (and react) installed in your app;
 * sending needs nothing extra beyond this package.
 */
import type { ReactElement } from "react";

export type Env = Record<string, string | undefined>;

export interface Attachment {
  filename: string;
  content: string | Uint8Array | ArrayBuffer;
  contentType?: string;
}

export interface EmailMessage {
  to: string | string[];
  subject: string;
  /** A react-email component; rendered to HTML and a plain-text alternative. */
  react?: ReactElement;
  html?: string;
  text?: string;
  /** Default: EMAIL_FROM, the project's address (e.g. shop@<box domain>). */
  from?: string;
  cc?: string | string[];
  bcc?: string | string[];
  replyTo?: string;
  headers?: Record<string, string>;
  attachments?: Attachment[];
}

export interface SendResult {
  /** The Message-ID header of the sent message. */
  messageId: string;
  /** Recipients the box accepted. */
  accepted: string[];
  /** Recipients refused, e.g. because they are on the suppression list. */
  rejected: string[];
  /** The server's reply. */
  response: string;
}

export interface SendOptions {
  /** smtp://user:pass@host:port; default process.env.SMTP_URL. */
  smtpUrl?: string;
  env?: Env;
}

export interface RenderOptions {
  /** Render plain text instead of HTML. */
  plainText?: boolean;
  /** Pretty-print the HTML. */
  pretty?: boolean;
}

/** Renders a react-email component to an HTML (or plain-text) string. */
export async function render(element: ReactElement, options?: RenderOptions): Promise<string> {
  type Renderer = { render: (el: ReactElement, o?: RenderOptions) => string | Promise<string> };
  let mod: Renderer;
  try {
    mod = (await import("@react-email/render")) as unknown as Renderer;
  } catch {
    throw new Error("render() needs @react-email/render: bun add @react-email/render react react-dom");
  }
  return await mod.render(element, options);
}

function list(v: string | string[] | undefined): string[] {
  if (v === undefined) return [];
  return Array.isArray(v) ? v : [v];
}

/** Sends a message through the box (or captures it in the dev inbox). */
export async function send(msg: EmailMessage, options?: SendOptions): Promise<SendResult> {
  const env = options?.env ?? process.env;
  const url = options?.smtpUrl ?? env.SMTP_URL;
  if (!url) {
    throw new Error("SMTP_URL is not set: add services.email to tiffin.config.ts (the box then sets it for every app), or pass smtpUrl");
  }
  const from = msg.from ?? env.EMAIL_FROM;
  if (!from) throw new Error("no from address: pass from, or set EMAIL_FROM");
  if (list(msg.to).length === 0) throw new Error("to: at least one recipient is required");
  let { html, text } = msg;
  if (msg.react) {
    html ??= await render(msg.react);
    text ??= await render(msg.react, { plainText: true });
  }
  if (!html && !text) throw new Error("send html, text or react");

  const nodemailer = await import("nodemailer");
  const transport = nodemailer.createTransport(url);
  try {
    const info = await transport.sendMail({
      from,
      to: list(msg.to),
      cc: list(msg.cc),
      bcc: list(msg.bcc),
      ...(msg.replyTo ? { replyTo: msg.replyTo } : {}),
      subject: msg.subject,
      ...(html ? { html } : {}),
      ...(text ? { text } : {}),
      ...(msg.headers ? { headers: msg.headers } : {}),
      attachments: (msg.attachments ?? []).map((a) => ({
        filename: a.filename,
        content: typeof a.content === "string" ? a.content : Buffer.from(a.content as ArrayBuffer),
        ...(a.contentType ? { contentType: a.contentType } : {}),
      })),
    });
    const addr = (x: unknown) => (typeof x === "string" ? x : (x as { address: string }).address);
    return {
      messageId: info.messageId,
      accepted: (info.accepted ?? []).map(addr),
      rejected: (info.rejected ?? []).map(addr),
      response: info.response,
    };
  } finally {
    transport.close();
  }
}
