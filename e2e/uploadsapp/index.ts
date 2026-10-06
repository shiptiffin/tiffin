// The app TestStorageUploads deploys: a page that uploads files from the
// browser with tiffin-sdk/storage/client, the upload routes that hand out
// tickets, and a queue handler that records object.created events.
// sdk.gen.js and public/client.js are bundled by the test (bun build).
import { onUploadCompleted, uploadRoute } from "./sdk.gen.js";

const events: unknown[] = [];
const media = uploadRoute({ bucket: "media", maxSize: 300 << 20, allowedTypes: ["image/*", "video/*"] });
// No size check of its own: the box enforces the bucket's maxFileSize.
const small = uploadRoute({ bucket: "small" });
const uploaded = onUploadCompleted((e) => {
  events.push(e);
  return { recorded: e.key };
});

const page = `<!doctype html><meta charset="utf-8"><title>uploads</title><script src="/client.js"></script><p>ready</p>`;

Bun.serve({
  port: Number(process.env.PORT ?? 3000),
  maxRequestBodySize: 1 << 20,
  async fetch(req) {
    const { pathname } = new URL(req.url);
    if (pathname === "/") return new Response(page, { headers: { "content-type": "text/html; charset=utf-8" } });
    if (pathname === "/client.js") return new Response(Bun.file("public/client.js"), { headers: { "content-type": "text/javascript" } });
    if (pathname === "/api/upload/media" && req.method === "POST") return media(req);
    if (pathname === "/api/upload/small" && req.method === "POST") return small(req);
    if (pathname === "/queues/uploads") return uploaded(req);
    if (pathname === "/events") return Response.json(events);
    return new Response("not found", { status: 404 });
  },
});
