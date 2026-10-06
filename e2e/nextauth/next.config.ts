import type { NextConfig } from "next";

export default {
  // forbidden() and unauthorized() (requireRole, verifySession({ signIn: false })).
  experimental: { authInterrupts: true },
  // A page that says who may frame it: the box's edge must not add X-Frame-Options.
  headers: async () => [{ source: "/embed", headers: [{ key: "Content-Security-Policy", value: "frame-ancestors https://partner.example" }] }],
} satisfies NextConfig;
