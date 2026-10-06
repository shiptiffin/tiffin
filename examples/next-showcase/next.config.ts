import type { NextConfig } from "next";

// No cache handler, compression or deploymentId here: the box's adapter sets
// them (see docs/guide/apps.md#nextjs).
const config: NextConfig = {
  cacheComponents: true,
  images: {
    // Public files of the project's buckets (TIFFIN_FILES_URL); the host
    // depends on the box's domain.
    remotePatterns: [{ protocol: "https", hostname: "**", pathname: "/next-showcase/media/**" }],
  },
};

export default config;
