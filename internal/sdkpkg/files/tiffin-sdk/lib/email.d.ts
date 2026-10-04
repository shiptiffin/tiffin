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
export declare function render(element: ReactElement, options?: RenderOptions): Promise<string>;
/** Sends a message through the box (or captures it in the dev inbox). */
export declare function send(msg: EmailMessage, options?: SendOptions): Promise<SendResult>;
