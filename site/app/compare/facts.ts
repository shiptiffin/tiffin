// Numbers the compare pages share. Every vendor number was read on the vendor's own page on
// READ_ISO. Re-check each one there, and change READ_ISO, before changing a number.
// Our own side comes from docs/guide (managed.md, limits.md) and app/pricing.tsx.

export const READ_ISO = "2026-10-10";
export const READ = "10 October 2026";

export const SRC = {
  coolifyPricing: "https://coolify.io/pricing",
  coolifyHome: "https://coolify.io",
  coolifyInstall: "https://coolify.io/docs/get-started/installation",
  coolifyDbBackups: "https://coolify.io/docs/databases/backups",
  coolifyVolumeBackups: "https://coolify.io/docs/core/persistent-storage/storage-mounts/backups",
  coolifyServices: "https://coolify.io/docs/services/overview",
  coolifyRepo: "https://github.com/coollabsio/coolify",
  vercelPricing: "https://vercel.com/pricing",
  vercelEnvironments: "https://vercel.com/docs/deployments/environments",
  supabasePricing: "https://supabase.com/pricing",
  supabaseSelfHost: "https://supabase.com/docs/guides/self-hosting/docker",
  supabaseRepo: "https://github.com/supabase/supabase",
  sentryPricing: "https://sentry.io/pricing/",
  hetznerPrices: "https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/",
  hetznerIpv4: "https://docs.hetzner.com/general/infrastructure-and-availability/ipv4-pricing",
  tiffinRepo: "https://github.com/shiptiffin/tiffin",
} as const;

/** ShipTiffin's fee, per box a month. */
export const FEE = 19;
export const FOUNDING = 12;

/**
 * The server, billed by Hetzner, in USD a month before VAT: the server type (Hetzner's
 * price list from 15 June 2026, Germany and Finland), a primary IPv4 ($0.60) and the
 * 40 GB data volume (about $2.80, Hetzner API, 8 October 2026). Rounded.
 */
export const BOXES = {
  cx23: { name: "CX23", spec: "2 vCPU, 4 GB", server: 6.49, total: 10 },
  cx33: { name: "CX33", spec: "4 vCPU, 8 GB", server: 9.99, total: 13 },
  cx43: { name: "CX43", spec: "8 vCPU, 16 GB", server: 18.49, total: 22 },
} as const;

export const usd = (n: number) => `$${n.toLocaleString("en-US")}`;
