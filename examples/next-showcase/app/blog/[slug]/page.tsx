import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { getPost, postSlugs } from "@/lib/data";

// Prerendered at build for the known posts, then regenerated in the
// background (ISR, see getPost) or on revalidatePath from the dashboard.
export function generateStaticParams() {
  return postSlugs().map((slug) => ({ slug }));
}

export async function generateMetadata({ params }: PageProps<"/blog/[slug]">): Promise<Metadata> {
  const post = await getPost((await params).slug);
  return { title: post?.title ?? "Not found" };
}

export default async function BlogPost({ params }: PageProps<"/blog/[slug]">) {
  const post = await getPost((await params).slug);
  if (!post) notFound();
  return (
    <main>
      <article>
        <h1 id="post-title">{post.title}</h1>
        <p className="muted">
          Revision <span id="post-rev">{post.rev}</span>, rendered at <time id="rendered-at">{post.at}</time>
        </p>
        <p>{post.body}</p>
      </article>
    </main>
  );
}
