import type { MetadataRoute } from "next";

export const dynamic = "force-static";

// Everyone may read the site, search and AI crawlers included (named so a later
// tightening of "*" can't drop them by accident). Signed-in pages are noindex anyway.
export default function robots(): MetadataRoute.Robots {
  const disallow = ["/api/", "/account", "/admin", "/sign-in"];
  return {
    rules: [
      { userAgent: "*", allow: "/", disallow },
      {
        userAgent: ["Googlebot", "Bingbot", "OAI-SearchBot", "ChatGPT-User", "Claude-SearchBot", "Claude-User", "PerplexityBot", "Perplexity-User"],
        allow: "/",
        disallow,
      },
      { userAgent: ["GPTBot", "ClaudeBot", "Google-Extended", "Applebot-Extended", "CCBot"], allow: "/", disallow },
    ],
    sitemap: "https://shiptiffin.com/sitemap.xml",
  };
}
