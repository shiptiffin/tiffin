// The seed data: written to Postgres by the release command, and what the
// first build prerenders, before the release has created the tables.

export type Product = { id: number; slug: string; name: string; price: number; stock: number; likes: number };
export type Post = { slug: string; title: string; body: string; rev: number };

export const PRODUCTS: Product[] = [
  ["lunch-box", "Steel lunch box", 2400],
  ["thermos", "Insulated flask", 1900],
  ["napkins", "Linen napkins (4)", 1200],
  ["spoon-set", "Bamboo spoon set", 900],
  ["tote", "Canvas tote", 1500],
  ["bento", "Three-tier bento", 3200],
  ["chopsticks", "Walnut chopsticks", 1100],
  ["jar", "Glass jar (1 l)", 800],
  ["wrap", "Beeswax wraps (3)", 1300],
  ["mug", "Enamel mug", 1000],
  ["board", "Oak cutting board", 2800],
  ["knife", "Paring knife", 2100],
].map(([slug, name, price], i) => ({ id: i + 1, slug, name, price, stock: 10 + i, likes: 0 }) as Product);

const para =
  "One machine runs the app, the database, the cache and the files. Requests stay on the box, " +
  "so a page that reads three tables is three local round trips, not three trips across a region.";

export const POSTS: Post[] = [
  { slug: "hello-box", title: "Hello from the box", body: para, rev: 1 },
  { slug: "caching-notes", title: "Notes on caching", body: para, rev: 1 },
  { slug: "streaming", title: "Streaming the slow parts", body: para, rev: 1 },
];
