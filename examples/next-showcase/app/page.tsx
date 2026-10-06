import type { Metadata } from "next";
import Image from "next/image";
import Link from "next/link";
import hero from "../public/hero.jpg";

export const metadata: Metadata = {
  title: "Home",
  description: "A store, a blog and a dashboard on one Tiffin box.",
  openGraph: { title: "Showcase on Tiffin", description: "Next.js 16 on one box." },
};

// The featured image lives in the project's public bucket (the release puts
// it there); TIFFIN_FILES_URL, set at build too, is where the box serves them.
const files = process.env.TIFFIN_FILES_URL;

export default function Home() {
  return (
    <main>
      <h1>Everything on one box</h1>
      <p className="muted">Static page: next/font, next/image (local and from the bucket), Open Graph image.</p>
      <Image
        id="hero"
        className="hero"
        src={hero}
        alt="A soft gradient"
        placeholder="blur"
        priority
        sizes="(max-width: 960px) 100vw, 912px"
      />
      <div className="split" style={{ marginTop: 32 }}>
        <div>
          <h2>Featured</h2>
          <p>Served from the media bucket and resized by next/image on the box.</p>
          <p>
            <Link href="/products">See the products</Link> or read <Link href="/blog/hello-box">the blog</Link>.
          </p>
        </div>
        <Image
          id="featured"
          className="hero"
          src={`${files}/media/featured.jpg`}
          alt="Featured product"
          width={1200}
          height={800}
          sizes="(max-width: 720px) 100vw, 456px"
        />
      </div>
    </main>
  );
}
