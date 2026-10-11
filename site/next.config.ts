import type { NextConfig } from "next";

// A server build: the pages are static (prerendered at build time), and the
// invite form adds a few route handlers that talk to Postgres and send
// mail through the box.
const config: NextConfig = {
  reactStrictMode: true,
  poweredByHeader: false,
  serverExternalPackages: ["postgres", "nodemailer", "@shiptiffin/sdk"],
  // Each guide is a page and a Markdown file. Coding agents ask for Markdown with
  // Accept: text/markdown and get it at the page's own address.
  async headers() {
    return [
      {
        source: "/docs/:slug([a-z0-9-]+).md",
        headers: [
          { key: "Link", value: '<https://shiptiffin.com/docs/:slug>; rel="canonical"' },
          { key: "Content-Type", value: "text/markdown; charset=utf-8" },
          { key: "Vary", value: "Accept" },
        ],
      },
      {
        source: "/docs/:slug([a-z0-9-]+)",
        headers: [
          { key: "Vary", value: "Accept" },
          { key: "Link", value: '</docs/:slug.md>; rel="alternate"; type="text/markdown"' },
        ],
      },
      { source: "/index.md", headers: [{ key: "Content-Type", value: "text/markdown; charset=utf-8" }, { key: "Link", value: '<https://shiptiffin.com/>; rel="canonical"' }] },
      { source: "/llms-full.txt", headers: [{ key: "X-Robots-Tag", value: "noindex" }] },
    ];
  },
  async redirects() {
    return [{ source: "/managed", destination: "/security", permanent: true }];
  },
  async rewrites() {
    const md = [{ type: "header" as const, key: "accept", value: "(.*)text/markdown(.*)" }];
    return {
      beforeFiles: [
        { source: "/docs/:slug([a-z0-9-]+)", has: md, destination: "/docs/:slug.md" },
        { source: "/docs", has: md, destination: "/llms.txt" },
        { source: "/", has: md, destination: "/index.md" },
      ],
      afterFiles: [],
      fallback: [],
    };
  },
};

export default config;
