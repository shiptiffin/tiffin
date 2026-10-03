import "./globals.css";

export const metadata = { title: "Notes · Next.js on Tiffin" };

export default function RootLayout({ children }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
