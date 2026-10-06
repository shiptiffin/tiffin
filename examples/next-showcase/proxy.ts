import { NextResponse, type NextRequest } from "next/server";

// Marks every response it handles, and sends visitors without a session
// cookie from /dashboard to /login (no real auth yet).
export function proxy(req: NextRequest) {
  if (req.nextUrl.pathname.startsWith("/dashboard") && !req.cookies.has("showcase_session")) {
    const res = NextResponse.redirect(new URL("/login", req.url));
    res.headers.set("x-showcase-proxy", "1");
    return res;
  }
  const res = NextResponse.next();
  res.headers.set("x-showcase-proxy", "1");
  return res;
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico|api/health).*)"],
};
