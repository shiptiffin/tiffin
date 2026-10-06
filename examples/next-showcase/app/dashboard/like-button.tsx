"use client";

import { useOptimistic, useState, useTransition } from "react";
import { likeProduct } from "../actions";

export function LikeButton({ id, likes }: { id: number; likes: number }) {
  const [count, setCount] = useState(likes);
  const [shown, bump] = useOptimistic(count, (n: number) => n + 1);
  const [, start] = useTransition();
  return (
    <button
      className="ghost"
      data-like={id}
      onClick={() =>
        start(async () => {
          bump(undefined);
          setCount(await likeProduct(id));
        })
      }
    >
      ♥ <span data-likes>{shown}</span>
    </button>
  );
}
