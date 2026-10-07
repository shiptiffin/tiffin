import type { NextConfig } from "next";

// A static export: `next build` writes out/, and the box serves those files
// from its edge (no container). Every page here is known at build time.
const config: NextConfig = {
  output: "export",
  images: { unoptimized: true },
  reactStrictMode: true,
  poweredByHeader: false,
};

export default config;
