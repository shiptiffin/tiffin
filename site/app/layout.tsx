import type { Metadata, Viewport } from "next";
import { Footer, Header } from "./chrome";
import { mono, sans, serif } from "./fonts";
import "./globals.css";

const description =
  "ShipTiffin sets up a server in your own Hetzner account with a database, auth, file storage, jobs, analytics, error tracking and backups already on it. Run all your apps on it, each with a hard limit, for $19 a month plus the server.";

export const metadata: Metadata = {
  metadataBase: new URL("https://shiptiffin.com"),
  title: { default: "ShipTiffin: all your apps, one box, one price", template: "%s · ShipTiffin" },
  description,
  applicationName: "ShipTiffin",
  alternates: { canonical: "/" },
  openGraph: {
    type: "website",
    siteName: "ShipTiffin",
    url: "https://shiptiffin.com",
    title: "ShipTiffin: all your apps, one box, one price",
    description,
    locale: "en",
  },
  twitter: { card: "summary_large_image" },
  formatDetection: { telephone: false, email: false, address: false },
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#f8f6f1" },
    { media: "(prefers-color-scheme: dark)", color: "#191816" },
  ],
  colorScheme: "light dark",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`${serif.variable} ${sans.variable} ${mono.variable}`}>
      <body>
        <a href="#main" className="skip">
          Skip to content
        </a>
        <Header />
        <main id="main">{children}</main>
        <Footer />
      </body>
    </html>
  );
}
