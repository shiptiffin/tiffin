export const metadata = { title: "hello-next on Tiffin" };

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body style={{ fontFamily: "system-ui, sans-serif", maxWidth: "40rem", margin: "4rem auto", padding: "0 1rem" }}>
        {children}
      </body>
    </html>
  );
}
