import type { MetadataRoute } from "next";

export const dynamic = "force-static";

export default function sitemap(): MetadataRoute.Sitemap {
  const lastModified = "2026-10-07";
  return [
    { url: "https://shiptiffin.com/", lastModified },
    { url: "https://shiptiffin.com/privacy", lastModified },
    { url: "https://shiptiffin.com/terms", lastModified },
  ];
}
