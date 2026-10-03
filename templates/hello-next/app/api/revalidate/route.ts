import { revalidateTag } from "next/cache";

// Expires everything tagged "time" on every instance (the tag lives in Valkey).
export async function POST() {
  revalidateTag("time", { expire: 0 });
  return Response.json({ revalidated: "time", at: new Date().toISOString() });
}
