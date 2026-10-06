import { getSession } from "@shiptiffin/sdk/next/auth";

export async function GET() {
  return Response.json(await getSession());
}
