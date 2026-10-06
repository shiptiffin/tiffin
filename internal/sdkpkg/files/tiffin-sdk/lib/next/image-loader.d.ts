/**
 * `tiffin-sdk/next/image-loader`: a next/image loader that has the box
 * resize images stored in buckets (files.<domain>/...?w=&q=&f=webp), so the
 * app does not run sharp. Images elsewhere are left as they are.
 *
 * ```ts
 * // image-loader.ts
 * export { default } from "tiffin-sdk/next/image-loader";
 * // next.config.ts
 * images: { loader: "custom", loaderFile: "./image-loader.ts" },
 * // a page
 * <Image src={publicUrl("assets", "hero.jpg")} width={1200} height={600} alt="" />
 * ```
 *
 * Private buckets: pass a signedUrl(); the loader adds the size to it. It
 * runs in the browser too, so it reads nothing from the environment.
 */
export interface ImageLoaderProps {
    src: string;
    width: number;
    quality?: number;
}
export default function tiffinImageLoader({ src, width, quality }: ImageLoaderProps): string;
