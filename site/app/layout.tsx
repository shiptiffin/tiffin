import type { Metadata, Viewport } from "next";
import { Footer, Header } from "./chrome";
import { mono, sans, serif } from "./fonts";
import "./globals.css";

const description =
  "A server of your own with a database, sign-in, email, file storage and background jobs already on it. Run all your apps on it for one flat price, each with the limit you choose.";

export const metadata: Metadata = {
  metadataBase: new URL("https://shiptiffin.com"),
  title: { default: "ShipTiffin: all your apps, one server, one price", template: "%s · ShipTiffin" },
  description,
  applicationName: "ShipTiffin",
  alternates: { canonical: "/" },
  openGraph: {
    type: "website",
    siteName: "ShipTiffin",
    url: "https://shiptiffin.com",
    title: "ShipTiffin: all your apps, one server, one price",
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
