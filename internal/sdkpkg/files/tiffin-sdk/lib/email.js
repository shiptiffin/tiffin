/** Renders a react-email component to an HTML (or plain-text) string. */
export async function render(element, options) {
    let mod;
    try {
        mod = (await import("@react-email/render"));
    }
    catch {
        throw new Error("render() needs @react-email/render: bun add @react-email/render react react-dom");
    }
    return await mod.render(element, options);
}
function list(v) {
    if (v === undefined)
        return [];
    return Array.isArray(v) ? v : [v];
}
/** Sends a message through the box (or captures it in the dev inbox). */
export async function send(msg, options) {
    const env = options?.env ?? process.env;
    const url = options?.smtpUrl ?? env.SMTP_URL;
    if (!url) {
        throw new Error("SMTP_URL is not set: add services.email to tiffin.config.ts (the box then sets it for every app), or pass smtpUrl");
    }
    const from = msg.from ?? env.EMAIL_FROM;
    if (!from)
        throw new Error("no from address: pass from, or set EMAIL_FROM");
    if (list(msg.to).length === 0)
        throw new Error("to: at least one recipient is required");
    let { html, text } = msg;
    if (msg.react) {
        html ??= await render(msg.react);
        text ??= await render(msg.react, { plainText: true });
    }
    if (!html && !text)
        throw new Error("send html, text or react");
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
                content: typeof a.content === "string" ? a.content : Buffer.from(a.content),
                ...(a.contentType ? { contentType: a.contentType } : {}),
            })),
        });
        const addr = (x) => (typeof x === "string" ? x : x.address);
        return {
            messageId: info.messageId,
            accepted: (info.accepted ?? []).map(addr),
            rejected: (info.rejected ?? []).map(addr),
            response: info.response,
        };
    }
    finally {
        transport.close();
    }
}
