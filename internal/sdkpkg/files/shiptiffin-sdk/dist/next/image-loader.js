/**
 * `@shiptiffin/sdk/next/image-loader`: a next/image loader that has the box
 * resize images stored in buckets (files.<domain>/...?w=&q=&f=webp), so the
 * app does not run sharp. Images elsewhere are left as they are.
 *
 * ```ts
 * // image-loader.ts
 * export { default } from "@shiptiffin/sdk/next/image-loader";
 * // next.config.ts
 * images: { loader: "custom", loaderFile: "./image-loader.ts" },
 * // a page
 * <Image src={publicUrl("assets", "hero.jpg")} width={1200} height={600} alt="" />
 * ```
 *
 * Private buckets: pass a signedUrl(); the loader adds the size to it. It
 * runs in the browser too, so it reads nothing from the environment.
 */
const widths = [16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840];
const qualities = [50, 75, 90, 100];
/** Whether src is a file on the box's files.<domain>. */
function isBoxFile(u) {
    return u.hostname.startsWith("files.") && (u.protocol === "https:" || u.protocol === "http:");
}
export default function tiffinImageLoader({ src, width, quality }) {
    let u;
    try {
        u = new URL(src);
    }
    catch {
        return src; // a path in the app itself
    }
    if (!isBoxFile(u))
        return src;
    const q = quality ?? 75;
    u.searchParams.set("w", String(widths.find((w) => w >= width) ?? widths[widths.length - 1]));
    u.searchParams.set("q", String(qualities.reduce((a, b) => (Math.abs(b - q) < Math.abs(a - q) ? b : a))));
    u.searchParams.set("f", "webp");
    return u.toString();
}
