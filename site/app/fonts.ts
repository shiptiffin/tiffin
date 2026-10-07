import localFont from "next/font/local";

// Self-hosted, OFL (licences in fonts/OFL-*.txt). The same three faces as the dashboard:
// Newsreader for sentences, Instrument Sans for the interface, Commit Mono for code.

export const serif = localFont({
  src: "./fonts/newsreader-latin-wght-normal.woff2",
  weight: "200 800",
  variable: "--font-serif",
  display: "swap",
  fallback: ["Georgia", "Times New Roman", "serif"],
});

export const sans = localFont({
  src: "./fonts/instrument-sans-latin-wght-normal.woff2",
  weight: "400 700",
  variable: "--font-sans",
  display: "swap",
  fallback: ["system-ui", "-apple-system", "Segoe UI", "sans-serif"],
});

export const mono = localFont({
  src: "./fonts/commit-mono-latin-400-normal.woff2",
  weight: "400",
  variable: "--font-mono",
  display: "swap",
  preload: false,
  fallback: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
});
