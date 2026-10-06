import { readFile } from "node:fs/promises";
import { join } from "node:path";

// The same JPEG under endless URLs: /_next/image?url=/api/swatch/<n> is a
// new image to optimize for every n (the bench's cold next/image test).
let bytes: Promise<Buffer> | undefined;

export async function GET(_req: Request, ctx: RouteContext<"/api/swatch/[n]">) {
  await ctx.params;
  bytes ??= readFile(join(process.cwd(), "seed", "featured.jpg"));
  return new Response(new Uint8Array(await bytes), {
    headers: { "content-type": "image/jpeg", "cache-control": "public, max-age=3600" },
  });
}
