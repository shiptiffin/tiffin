import type { Metadata } from "next";
import { cookies } from "next/headers";
import { Suspense } from "react";
import { getProducts } from "@/lib/data";
import { AddToCart } from "./add-to-cart";

export const metadata: Metadata = { title: "Products" };

// Partial prerendering: the product grid is cached ("use cache", tag
// "products") and part of the static shell; the cart count reads a cookie,
// so it is a dynamic hole streamed in per request.
export default function Products() {
  return (
    <main>
      <div className="row">
        <h1>Products</h1>
        <Suspense fallback={<span id="cart-count" data-loading="true">Cart: …</span>}>
          <CartCount />
        </Suspense>
      </div>
      <ProductGrid />
    </main>
  );
}

async function CartCount() {
  const n = Number((await cookies()).get("cart")?.value ?? 0) || 0;
  return <span id="cart-count">Cart: {n}</span>;
}

async function ProductGrid() {
  const { products, at } = await getProducts();
  return (
    <>
      <p className="muted">
        Cached at <time id="products-at">{at}</time>
      </p>
      <div className="grid">
        {products.map((p) => (
          <div className="card" key={p.id} data-product={p.slug}>
            <strong>{p.name}</strong>
            <p className="muted">
              ${(p.price / 100).toFixed(2)} · <span data-stock>{p.stock}</span> in stock
            </p>
            <AddToCart />
          </div>
        ))}
      </div>
    </>
  );
}
