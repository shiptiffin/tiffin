"use client";

import { useTransition } from "react";
import { addToCart } from "../actions";

export function AddToCart() {
  const [pending, start] = useTransition();
  return (
    <button className="ghost" disabled={pending} onClick={() => start(() => addToCart())}>
      {pending ? "Adding…" : "Add to cart"}
    </button>
  );
}
