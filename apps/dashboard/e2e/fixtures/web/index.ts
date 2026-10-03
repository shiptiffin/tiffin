import { Hono } from "hono";
import { logger } from "hono/logger";

// The demo shop's web app for the dashboard's seeded dev box.
const app = new Hono();
app.use(logger());

const products = [
  { sku: "BWL-01", name: "Speckled ceramic bowl", price: 3200 },
  { sku: "TIF-03", name: "Three-tier tiffin, brass", price: 5400 },
  { sku: "TEA-04", name: "Hojicha, 100 g", price: 1400 },
];

const page = (title: string, body: string) =>
  `<!doctype html><html><head><meta charset="utf-8"><title>${title} · Shop</title></head><body style="font-family:Georgia,serif;max-width:640px;margin:48px auto;padding:0 20px;color:#2b2520"><h1>${title}</h1>${body}</body></html>`;

app.get("/", (c) => c.html(page(process.env.GREETING ?? "Small things for slow lunches", `<p>${products.length} things in stock.</p>`)));
app.get("/shop", (c) => c.html(page("Shop", products.map((p) => `<p>${p.name}: $${(p.price / 100).toFixed(2)}</p>`).join(""))));
app.get("/shop/:slug", (c) => c.html(page(c.req.param("slug"), "<p>Handmade, in small batches.</p>")));
app.get("/api/products", (c) => c.json(products));
app.get("/healthz", (c) => c.text("ok"));
app.post("/api/checkout", async (c) => {
  const body = await c.req.json().catch(() => ({}));
  if (!Array.isArray((body as { items?: unknown }).items)) {
    console.error("checkout: no items in the request body");
    return c.json({ error: "items required" }, 400);
  }
  return c.json({ ok: true });
});

const port = Number(process.env.PORT ?? 3000);
console.log(`shop web listening on :${port}`);
export default { port, fetch: app.fetch };
