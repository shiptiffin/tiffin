import type { Metadata } from "next";
import { GeistSans } from "geist/font/sans";
import Link from "next/link";
import "./globals.css";

export const metadata: Metadata = {
  title: { default: "Showcase", template: "%s · Showcase" },
  description: "A store, a blog and a dashboard on one Tiffin box.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={GeistSans.className}>
      <body>
        <nav>
          <Link href="/">Home</Link>
          <Link href="/products">Products</Link>
          <Link href="/blog/hello-box">Blog</Link>
          <Link href="/dashboard">Dashboard</Link>
        </nav>
        {children}
      </body>
    </html>
  );
}
