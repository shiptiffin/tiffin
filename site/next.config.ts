import type { NextConfig } from "next";

// A server build: the pages are static (prerendered at build time), and the
// early-access list adds a few route handlers that talk to Postgres and send
// mail through the box.
const config: NextConfig = {
  reactStrictMode: true,
  poweredByHeader: false,
  serverExternalPackages: ["postgres", "nodemailer", "@shiptiffin/sdk"],
};

export default config;
