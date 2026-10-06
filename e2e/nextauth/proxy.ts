import { authProxy } from "tiffin-sdk/next/auth";

export const proxy = authProxy({ protect: ["/dashboard/:path*"] });

export const config = { matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"] };
