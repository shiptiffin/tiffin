import { useEffect, useEffectEvent, useState } from "react";

/** value, once it has stayed the same for ms. Compared by content (JSON), so a new object with the same data doesn't restart the wait. */
export function useDebounced<T>(value: T, ms: number): T {
  const key = JSON.stringify(value);
  const [v, setV] = useState(value);
  const settle = useEffectEvent(() => setV(value));
  useEffect(() => {
    const t = setTimeout(() => settle(), ms);
    return () => clearTimeout(t);
  }, [key, ms]);
  return v;
}
