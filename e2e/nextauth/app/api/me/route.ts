import { getSession } from "tiffin-sdk/next/auth";

export async function GET() {
  return Response.json(await getSession());
}
